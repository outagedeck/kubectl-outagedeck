# kubectl-outagedeck

[![CI](https://github.com/outagedeck/kubectl-outagedeck/actions/workflows/ci.yml/badge.svg)](https://github.com/outagedeck/kubectl-outagedeck/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Check whether the cloud and SaaS services behind Kubernetes workloads are reporting incidents. The plugin reads dependency annotations from Deployments, StatefulSets, and DaemonSets, then checks their normalized official status feeds through [OutageDeck](https://outagedeck.com?utm_source=krew&utm_medium=plugin&utm_campaign=krew_plugin).

OutageDeck complements cluster and synthetic monitoring. It never treats an unreachable or malformed provider response as operational.

## Install

Install the released plugin through its Krew manifest:

```bash
curl -fsSLO https://raw.githubusercontent.com/outagedeck/kubectl-outagedeck/main/krew/outagedeck.yaml
kubectl krew install --manifest=outagedeck.yaml
```

Or download a release archive for macOS, Linux, or Windows from the [releases page](https://github.com/outagedeck/kubectl-outagedeck/releases), or build from source:

```bash
go install github.com/outagedeck/kubectl-outagedeck/cmd/kubectl-outagedeck@latest
```

The central Krew-index submission is tracked in this repository's README after publication.

## Check named providers

No cluster access is needed when provider slugs are supplied explicitly:

```console
$ kubectl outagedeck aws cloudflare github openai
OK AWS: Operational — All Systems Operational
OK Cloudflare: Operational — All Systems Operational
OK GitHub: Operational — All Systems Operational
!! OpenAI: Degraded — OpenAI reports an active incident
```

Find provider slugs in the [OutageDeck provider catalog](https://outagedeck.com/providers?utm_source=krew&utm_medium=plugin&utm_campaign=krew_plugin).

## Discover workload dependencies

Annotate the workload object, not its pod template:

```bash
kubectl annotate deployment checkout-api \
  outagedeck.com/providers=aws,github,stripe
```

Check annotated workloads in the current namespace:

```bash
kubectl outagedeck
```

Or scope discovery with familiar kubectl flags:

```bash
kubectl outagedeck --namespace payments --selector app=checkout-api
kubectl outagedeck --all-namespaces
```

The repository includes an [annotated Deployment example](examples/deployment.yaml).

## Automation

Use structured output and a deliberate failure threshold in scripts:

```bash
kubectl outagedeck --all-namespaces --json --fail-on=outage
```

Exit codes:

- `0`: every provider is below the selected threshold;
- `1`: discovery, validation, or a provider request failed;
- `2`: at least one provider met the selected threshold.

`--fail-on` accepts `degraded` (default), `outage`, `major_outage`, or `never`. The anonymous API quota is 120 requests per hour and one invocation checks no more than 20 unique providers. Set `OUTAGEDECK_API_KEY` or use `--api-key` for a higher quota.

Run `kubectl outagedeck --help` for all namespace, selector, kubeconfig, context, output, and timeout options.

## Data and privacy

- Provider state comes from official vendor status feeds and is refreshed about every 10 minutes.
- The plugin sends only provider slugs to the OutageDeck public API. It does not send Kubernetes resource names, namespaces, labels, kubeconfig data, or cluster credentials.
- The plugin adds no telemetry. Requests identify the client through a standard `User-Agent` header.
- Annotation discovery is read-only and invokes the user's existing `kubectl` binary.

Review the [OutageDeck API documentation](https://outagedeck.com/developers/api?utm_source=krew&utm_medium=plugin&utm_campaign=krew_plugin) or [configure managed outage alerts](https://outagedeck.com/alerts?utm_source=krew&utm_medium=plugin&utm_campaign=krew_plugin).

## License

MIT
