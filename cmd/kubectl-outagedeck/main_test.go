package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func providerResponse(slug, name, status string) string {
	return fmt.Sprintf(`{"data":{"slug":%q,"name":%q,"currentStatus":{"code":%q,"label":%q,"headline":"Live headline"},"source":{"checkedAt":"2026-08-05T00:00:00Z"},"counts":{"activeIncidents":1}}}`, slug, name, status, status)
}

func TestExplicitProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.Header.Get("User-Agent"), "kubectl-outagedeck/") {
			t.Fatal("expected kubectl plugin user agent")
		}
		fmt.Fprint(writer, providerResponse("github", "GitHub", "operational"))
	}))
	defer server.Close()
	t.Setenv("OUTAGEDECK_API_BASE_URL", server.URL)

	var stdout, stderr bytes.Buffer
	exit := run([]string{"github"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), "OK GitHub: operational") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestAnnotationDiscoveryAndFailureThreshold(t *testing.T) {
	original := invokeKubectl
	defer func() { invokeKubectl = original }()
	invokeKubectl = func(_ context.Context, args []string) ([]byte, error) {
		want := []string{"get", defaultResources, "--output=json", "--ignore-not-found", "--namespace", "payments", "--selector", "tier=api"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("kubectl args = %#v, want %#v", args, want)
		}
		return []byte(`{"items":[
          {"kind":"Deployment","metadata":{"name":"checkout","namespace":"payments","annotations":{"outagedeck.com/providers":"AWS, github"}}},
          {"kind":"StatefulSet","metadata":{"name":"ledger","namespace":"payments","annotations":{"outagedeck.com/providers":"aws"}}}
        ]}`), nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		slug := strings.TrimPrefix(request.URL.Path, "/providers/")
		status := "operational"
		if slug == "aws" {
			status = "partial_outage"
		}
		fmt.Fprint(writer, providerResponse(slug, strings.ToUpper(slug), status))
	}))
	defer server.Close()
	t.Setenv("OUTAGEDECK_API_BASE_URL", server.URL)

	var stdout, stderr bytes.Buffer
	exit := run([]string{"--namespace", "payments", "--selector", "tier=api", "--json", "--fail-on", "outage"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", exit, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"status": "partial_outage"`) ||
		!strings.Contains(stdout.String(), `"Deployment/payments/checkout"`) ||
		!strings.Contains(stdout.String(), `"StatefulSet/payments/ledger"`) {
		t.Fatalf("unexpected JSON: %s", stdout.String())
	}
}

func TestNoAnnotations(t *testing.T) {
	original := invokeKubectl
	defer func() { invokeKubectl = original }()
	invokeKubectl = func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(`{"items":[{"kind":"Deployment","metadata":{"name":"web","namespace":"default"}}]}`), nil
	}
	var stdout, stderr bytes.Buffer
	exit := run(nil, &stdout, &stderr)
	if exit != 1 || !strings.Contains(stderr.String(), defaultAnnotation) {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
}

func TestInvalidAnnotation(t *testing.T) {
	payload := []byte(`{"items":[{"kind":"DaemonSet","metadata":{"name":"agent","namespace":"ops","annotations":{"outagedeck.com/providers":"bad slug"}}}]}`)
	_, err := targetsFromKubernetes(payload, defaultAnnotation)
	if err == nil || !strings.Contains(err.Error(), "DaemonSet/ops/agent") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAllNamespacesArguments(t *testing.T) {
	got := kubectlArgs(options{allNamespaces: true, context: "prod", kubeconfig: "/tmp/config"})
	want := []string{"get", defaultResources, "--output=json", "--ignore-not-found", "--all-namespaces", "--context", "prod", "--kubeconfig", "/tmp/config"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kubectl args = %#v, want %#v", got, want)
	}
}

func TestKubectlFailure(t *testing.T) {
	original := invokeKubectl
	defer func() { invokeKubectl = original }()
	invokeKubectl = func(_ context.Context, _ []string) ([]byte, error) {
		return nil, fmt.Errorf("cluster unavailable")
	}
	var stdout, stderr bytes.Buffer
	exit := run(nil, &stdout, &stderr)
	if exit != 1 || !strings.Contains(stderr.String(), "cluster unavailable") {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
}

func TestOptionsValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--namespace", "one", "--all-namespaces"},
		{"--fail-on", "sometimes", "github"},
		{"--timeout", "0s", "github"},
	} {
		var stdout, stderr bytes.Buffer
		if exit := run(args, &stdout, &stderr); exit != 1 {
			t.Fatalf("args = %#v, exit = %d", args, exit)
		}
	}
}

func TestVersionAndHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := run([]string{"--version"}, &stdout, &stderr); exit != 0 || !strings.Contains(stdout.String(), version) {
		t.Fatalf("version exit = %d, stdout = %s", exit, stdout.String())
	}
	stdout.Reset()
	if exit := run([]string{"--help"}, &stdout, &stderr); exit != 0 || !strings.Contains(stdout.String(), "kubectl outagedeck") {
		t.Fatalf("help exit = %d, stdout = %s", exit, stdout.String())
	}
}
