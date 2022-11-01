# JIRAlert
[![Build Status](https://github.com/prometheus-community/jiralert/workflows/test/badge.svg?branch=master)](https://github.com/prometheus-community/jiralert/actions?query=workflow%3Atest) 
[![Go Report Card](https://goreportcard.com/badge/github.com/prometheus-community/jiralert)](https://goreportcard.com/report/github.com/prometheus-community/jiralert) 
[Prometheus Alertmanager](https://github.com/prometheus/alertmanager) webhook receiver for [JIRA](https://www.atlassian.com/software/jira).

## Overview

JIRAlert implements Alertmanager's webhook HTTP API and connects to one or more JIRA instances to create highly configurable JIRA issues. One issue is created per distinct group key — as defined by the [`group_by`](https://prometheus.io/docs/alerting/configuration/#<route>) parameter of Alertmanager's `route` configuration section — but not closed when the alert is resolved. The expectation is that a human will look at the issue, take any necessary action, then close it.  If no human interaction is necessary then it should probably not alert in the first place. This behavior however can be modified by setting `auto_resolve` section, which will resolve the jira issue with required state.

If a corresponding JIRA issue already exists but is resolved, it is reopened. A JIRA transition must exist between the resolved state and the reopened state — as defined by `reopen_state` — or reopening will fail. Optionally a "won't fix" resolution — defined by `wont_fix_resolution` — may be defined: a JIRA issue with this resolution will not be reopened by JIRAlert.

## Usage

### Install with Helm

```shell
helm install ./helm/jiralert -n {your-namespace}
```

### Install with Binary

```bash

Get JIRAlert, either as a [packaged release](https://github.com/prometheus-community/jiralert/releases) or build it yourself:

```
$ go get github.com/prometheus-community/jiralert/cmd/jiralert
```

then run it from the command line:

```
$ jiralert
```

Use the `-help` flag to get help information.

```
$ jiralert -help
Usage of jiralert:
  -config string
      The JIRAlert configuration file (default "config/jiralert.yml")
  -listen-address string
      The address to listen on for HTTP requests. (default ":9097")
  [...]
```

