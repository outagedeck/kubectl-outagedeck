package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

var version = "dev"

const (
	defaultAPIBase    = "https://outagedeck.com/api/v1"
	defaultAnnotation = "outagedeck.com/providers"
	defaultResources  = "deployments.apps,statefulsets.apps,daemonsets.apps"
)

type currentStatus struct {
	Code     string `json:"code"`
	Label    string `json:"label"`
	Headline string `json:"headline"`
	Summary  string `json:"summary"`
}

type source struct {
	CheckedAt string `json:"checkedAt"`
}

type counts struct {
	ActiveIncidents int `json:"activeIncidents"`
}

type provider struct {
	Slug          string        `json:"slug"`
	Name          string        `json:"name"`
	CurrentStatus currentStatus `json:"currentStatus"`
	Source        source        `json:"source"`
	Counts        counts        `json:"counts"`
}

type providerEnvelope struct {
	Data provider `json:"data"`
}

type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

type result struct {
	Slug            string   `json:"slug"`
	Name            string   `json:"name"`
	Status          string   `json:"status"`
	Label           string   `json:"label"`
	Headline        string   `json:"headline,omitempty"`
	ActiveIncidents int      `json:"activeIncidents"`
	CheckedAt       string   `json:"checkedAt,omitempty"`
	Resources       []string `json:"resources,omitempty"`
	URL             string   `json:"url"`
	Error           string   `json:"error,omitempty"`
}

type objectMeta struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Annotations map[string]string `json:"annotations"`
}

type kubeObject struct {
	Kind     string     `json:"kind"`
	Metadata objectMeta `json:"metadata"`
}

type kubeList struct {
	Items []kubeObject `json:"items"`
}

type target struct {
	Slug      string
	Resources []string
}

type options struct {
	allNamespaces bool
	annotation    string
	apiKey        string
	context       string
	failOn        string
	jsonOutput    bool
	kubeconfig    string
	namespace     string
	selector      string
	timeout       time.Duration
}

func apiBase() string {
	if value := strings.TrimRight(os.Getenv("OUTAGEDECK_API_BASE_URL"), "/"); value != "" {
		return value
	}
	return defaultAPIBase
}

func requestJSON(ctx context.Context, httpClient *http.Client, endpoint, apiKey string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kubectl-outagedeck/"+version+" (+https://github.com/outagedeck/kubectl-outagedeck)")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	response, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload errorEnvelope
		_ = json.NewDecoder(response.Body).Decode(&payload)
		message := payload.Error.Message
		if message == "" {
			message = payload.Message
		}
		if message == "" {
			message = response.Status
		}
		return errors.New(message)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode OutageDeck response: %w", err)
	}
	return nil
}

func providerURL(slug string) string {
	return "https://outagedeck.com/providers/" + url.PathEscape(slug) +
		"?utm_source=krew&utm_medium=plugin&utm_campaign=krew_plugin"
}

func stackAlertsURL(slugs []string) string {
	query := url.Values{}
	query.Set("stack", strings.Join(slugs, ","))
	query.Set("utm_source", "krew")
	query.Set("utm_medium", "plugin")
	query.Set("utm_campaign", "krew_plugin")
	query.Set("utm_content", "alerts_command")
	return "https://outagedeck.com/account?" + query.Encode()
}

func fetchProvider(ctx context.Context, httpClient *http.Client, item target, apiKey string) result {
	var payload providerEnvelope
	endpoint := apiBase() + "/providers/" + url.PathEscape(item.Slug)
	if err := requestJSON(ctx, httpClient, endpoint, apiKey, &payload); err != nil {
		return result{Slug: item.Slug, Name: item.Slug, Status: "error", Resources: item.Resources, Error: err.Error(), URL: providerURL(item.Slug)}
	}
	data := payload.Data
	if data.Name == "" || data.CurrentStatus.Code == "" {
		return result{Slug: item.Slug, Name: item.Slug, Status: "error", Resources: item.Resources, Error: "unexpected provider response", URL: providerURL(item.Slug)}
	}
	headline := data.CurrentStatus.Headline
	if headline == "" {
		headline = data.CurrentStatus.Summary
	}
	label := data.CurrentStatus.Label
	if label == "" {
		label = data.CurrentStatus.Code
	}
	return result{
		Slug:            data.Slug,
		Name:            data.Name,
		Status:          data.CurrentStatus.Code,
		Label:           label,
		Headline:        headline,
		ActiveIncidents: data.Counts.ActiveIncidents,
		CheckedAt:       data.Source.CheckedAt,
		Resources:       item.Resources,
		URL:             providerURL(data.Slug),
	}
}

func normalizeSlugs(values []string) ([]string, error) {
	seen := make(map[string]bool)
	slugs := make([]string, 0, len(values))
	for _, value := range values {
		for _, raw := range strings.Split(value, ",") {
			slug := strings.ToLower(strings.TrimSpace(raw))
			if slug == "" || seen[slug] {
				continue
			}
			for i, char := range slug {
				valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-'
				if !valid || (char == '-' && (i == 0 || i == len(slug)-1)) {
					return nil, fmt.Errorf("invalid provider slug: %s", slug)
				}
			}
			seen[slug] = true
			slugs = append(slugs, slug)
		}
	}
	if len(slugs) == 0 {
		return nil, errors.New("no provider slugs found")
	}
	if len(slugs) > 20 {
		return nil, errors.New("at most 20 providers can be checked at once")
	}
	return slugs, nil
}

func resourceName(item kubeObject) string {
	kind := item.Kind
	if kind == "" {
		kind = "Resource"
	}
	if item.Metadata.Namespace == "" {
		return kind + "/" + item.Metadata.Name
	}
	return kind + "/" + item.Metadata.Namespace + "/" + item.Metadata.Name
}

func targetsFromKubernetes(payload []byte, annotation string) ([]target, error) {
	var list kubeList
	if err := json.Unmarshal(payload, &list); err != nil {
		return nil, fmt.Errorf("decode kubectl response: %w", err)
	}
	bySlug := make(map[string][]string)
	for _, item := range list.Items {
		value := item.Metadata.Annotations[annotation]
		if strings.TrimSpace(value) == "" {
			continue
		}
		slugs, err := normalizeSlugs([]string{value})
		if err != nil {
			return nil, fmt.Errorf("%s annotation on %s: %w", annotation, resourceName(item), err)
		}
		for _, slug := range slugs {
			bySlug[slug] = append(bySlug[slug], resourceName(item))
		}
	}
	if len(bySlug) == 0 {
		return nil, fmt.Errorf("no workloads carry the %s annotation", annotation)
	}
	slugs := make([]string, 0, len(bySlug))
	for slug := range bySlug {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	if len(slugs) > 20 {
		return nil, errors.New("annotations reference more than 20 providers; narrow the namespace or label selector")
	}
	targets := make([]target, 0, len(slugs))
	for _, slug := range slugs {
		resources := bySlug[slug]
		sort.Strings(resources)
		targets = append(targets, target{Slug: slug, Resources: resources})
	}
	return targets, nil
}

func kubectlArgs(opts options) []string {
	args := []string{"get", defaultResources, "--output=json", "--ignore-not-found"}
	if opts.allNamespaces {
		args = append(args, "--all-namespaces")
	} else if opts.namespace != "" {
		args = append(args, "--namespace", opts.namespace)
	}
	if opts.selector != "" {
		args = append(args, "--selector", opts.selector)
	}
	if opts.context != "" {
		args = append(args, "--context", opts.context)
	}
	if opts.kubeconfig != "" {
		args = append(args, "--kubeconfig", opts.kubeconfig)
	}
	return args
}

var invokeKubectl = func(ctx context.Context, args []string) ([]byte, error) {
	binary := strings.TrimSpace(os.Getenv("KUBECTL"))
	if binary == "" {
		binary = "kubectl"
	}
	command := exec.CommandContext(ctx, binary, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return nil, errors.New(message)
	}
	return output, nil
}

var statusRanks = map[string]int{
	"operational":    0,
	"maintenance":    1,
	"unknown":        1,
	"degraded":       2,
	"partial_outage": 3,
	"major_outage":   4,
}

var failureRanks = map[string]int{
	"degraded":     2,
	"outage":       3,
	"major_outage": 4,
	"never":        1 << 30,
}

func bindDiscoveryFlags(flags *flag.FlagSet, opts *options) {
	flags.BoolVar(&opts.allNamespaces, "all-namespaces", false, "inspect annotated workloads in every namespace")
	flags.BoolVar(&opts.allNamespaces, "A", false, "shorthand for --all-namespaces")
	flags.StringVar(&opts.annotation, "annotation", defaultAnnotation, "provider annotation key")
	flags.StringVar(&opts.context, "context", "", "kubectl context used for workload discovery")
	flags.StringVar(&opts.kubeconfig, "kubeconfig", "", "path to a kubeconfig file")
	flags.StringVar(&opts.namespace, "namespace", "", "namespace used for workload discovery")
	flags.StringVar(&opts.namespace, "n", "", "shorthand for --namespace")
	flags.StringVar(&opts.selector, "selector", "", "label selector used for workload discovery")
	flags.StringVar(&opts.selector, "l", "", "shorthand for --selector")
	flags.DurationVar(&opts.timeout, "timeout", 10*time.Second, "timeout for kubectl and each API request")
}

func validateDiscoveryOptions(opts options) error {
	if opts.namespace != "" && opts.allNamespaces {
		return errors.New("--namespace and --all-namespaces cannot be used together")
	}
	if strings.TrimSpace(opts.annotation) == "" {
		return errors.New("--annotation cannot be empty")
	}
	if opts.timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	return nil
}

func resolveTargets(args []string, opts options) ([]target, error) {
	if len(args) > 0 {
		slugs, err := normalizeSlugs(args)
		if err != nil {
			return nil, err
		}
		targets := make([]target, 0, len(slugs))
		for _, slug := range slugs {
			targets = append(targets, target{Slug: slug})
		}
		return targets, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	payload, err := invokeKubectl(ctx, kubectlArgs(opts))
	if err != nil {
		return nil, fmt.Errorf("kubectl discovery failed: %w", err)
	}
	return targetsFromKubernetes(payload, opts.annotation)
}

func check(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("outagedeck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var opts options
	bindDiscoveryFlags(flags, &opts)
	flags.StringVar(&opts.apiKey, "api-key", os.Getenv("OUTAGEDECK_API_KEY"), "optional OutageDeck API key")
	flags.StringVar(&opts.failOn, "fail-on", "degraded", "degraded, outage, major_outage, or never")
	flags.BoolVar(&opts.jsonOutput, "json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if err := validateDiscoveryOptions(opts); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	threshold, ok := failureRanks[strings.ToLower(opts.failOn)]
	if !ok {
		fmt.Fprintln(stderr, "--fail-on must be degraded, outage, major_outage, or never")
		return 1
	}

	targets, err := resolveTargets(flags.Args(), opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	ctx := context.Background()
	results := make([]result, len(targets))
	done := make(chan int, len(targets))
	httpClient := &http.Client{Timeout: opts.timeout}
	for index, item := range targets {
		go func(index int, item target) {
			results[index] = fetchProvider(ctx, httpClient, item, strings.TrimSpace(opts.apiKey))
			done <- index
		}(index, item)
	}
	for range targets {
		<-done
	}

	hasError := false
	hasFailure := false
	for _, item := range results {
		if item.Error != "" {
			hasError = true
			continue
		}
		if rank, exists := statusRanks[item.Status]; !exists || rank >= threshold {
			hasFailure = true
		}
	}

	if opts.jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(results); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		for _, item := range results {
			if item.Error != "" {
				fmt.Fprintf(stdout, "! %s: check failed: %s\n", item.Name, item.Error)
				continue
			}
			marker := "OK"
			if item.Status != "operational" {
				marker = "!!"
			}
			fmt.Fprintf(stdout, "%s %s: %s", marker, item.Name, item.Label)
			if item.Headline != "" {
				fmt.Fprintf(stdout, " — %s", item.Headline)
			}
			if len(item.Resources) > 0 {
				fmt.Fprintf(stdout, " [%s]", strings.Join(item.Resources, ", "))
			}
			fmt.Fprintln(stdout)
		}
	}
	if hasError {
		return 1
	}
	if hasFailure {
		return 2
	}
	return 0
}

func alertsCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("alerts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var opts options
	bindDiscoveryFlags(flags, &opts)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if err := validateDiscoveryOptions(opts); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	targets, err := resolveTargets(flags.Args(), opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	slugs := make([]string, len(targets))
	for index, item := range targets {
		slugs[index] = item.Slug
	}

	fmt.Fprintf(stdout, "Set up alerts for %s:\n", strings.Join(slugs, ", "))
	fmt.Fprintln(stdout, stackAlertsURL(slugs))
	fmt.Fprintln(stdout, "\nThe selected stack will already be filled in after sign-in.")
	fmt.Fprintln(stdout, "Free email alerts cover up to five providers.")
	return 0
}

func usage(writer io.Writer) {
	fmt.Fprintln(writer, `OutageDeck kubectl plugin — check external dependencies from official status feeds

Usage:
  kubectl outagedeck [flags] [provider ...]
  kubectl outagedeck alerts [flags] [provider ...]

With provider arguments, checks those providers directly. Without arguments,
discovers providers from outagedeck.com/providers annotations on Deployments,
StatefulSets, and DaemonSets in the current namespace.

Examples:
  kubectl outagedeck aws cloudflare github
  kubectl outagedeck --all-namespaces
  kubectl outagedeck --namespace payments --selector app=checkout
  kubectl outagedeck --json --fail-on=outage
  kubectl outagedeck alerts github cloudflare openai
  kubectl outagedeck alerts --namespace payments

Discovery flags:
  -n, --namespace string       namespace to inspect
  -A, --all-namespaces        inspect every namespace
  -l, --selector string       workload label selector
      --annotation string     annotation key (default outagedeck.com/providers)
      --context string        kubectl context
      --kubeconfig string     kubeconfig path

Check flags:
      --api-key string        optional higher-quota OutageDeck API key
      --fail-on string        degraded, outage, major_outage, or never
      --json                  machine-readable output
      --timeout duration      kubectl and HTTP timeout (default 10s)

Environment:
  OUTAGEDECK_API_KEY          optional higher-quota API key
  OUTAGEDECK_API_BASE_URL     API override for testing
  KUBECTL                     kubectl binary override for testing`)
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "alerts":
			return alertsCommand(args[1:], stdout, stderr)
		case "version", "--version", "-v":
			fmt.Fprintln(stdout, version)
			return 0
		case "help", "--help", "-h":
			usage(stdout)
			return 0
		}
	}
	return check(args, stdout, stderr)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
