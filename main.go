// Copyright 2017 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"fmt"
	"github.com/Hoverhuang-er/go-actuator"
	"net/http"
	"os"
	"runtime"
	"strconv"

	"github.com/andygrunwald/go-jira"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus-community/jiralert/pkg/alertmanager"
	"github.com/prometheus-community/jiralert/pkg/config"
	"github.com/prometheus-community/jiralert/pkg/notify"
	"github.com/prometheus-community/jiralert/pkg/template"
	_ "net/http/pprof"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	unknownReceiver = "<unknown>"
	logFormatLogfmt = "logfmt"
	logFormatJSON   = "json"
)

type Flg struct {
	Loglevel      string
	Logfmt        string
	Config        string
	ListenAddr    string
	HashJiraLabel bool
	Version       string
}

func main() {
	runtime.SetBlockProfileRate(1)
	runtime.SetMutexProfileFraction(1)
	fg := Flg{
		Loglevel:      "info",
		Logfmt:        logFormatLogfmt,
		Config:        "./jiralert.yml",
		ListenAddr:    ":8080",
		HashJiraLabel: false,
		Version:       "<local build>",
	}

	var logger = setupLogger(fg.Loglevel, fg.Logfmt)
	level.Info(logger).Log("msg", "starting JIRAlert", "version", fg.Version)

	if !fg.HashJiraLabel {
		level.Warn(logger).Log("msg", "Using deprecated jira label generation - "+
			"please read https://github.com/prometheus-community/jiralert/pull/79 "+
			"and try -hash-jira-label")
	}

	config, _, err := config.LoadFile(fg.Config, logger)
	if err != nil {
		level.Error(logger).Log("msg", "error loading configuration", "path", fg.Config, "err", err)
		os.Exit(1)
	}

	tmpl, err := template.LoadTemplate(config.Template, logger)
	if err != nil {
		level.Error(logger).Log("msg", "error loading templates", "path", config.Template, "err", err)
		os.Exit(1)
	}

	http.HandleFunc("/alert", func(w http.ResponseWriter, req *http.Request) {
		level.Debug(logger).Log("msg", "handling /alert webhook request")
		defer func() { _ = req.Body.Close() }()

		// https://godoc.org/github.com/prometheus/alertmanager/template#Data
		data := alertmanager.Data{}
		if err := json.NewDecoder(req.Body).Decode(&data); err != nil {
			errorHandler(w, http.StatusBadRequest, err, unknownReceiver, &data, logger)
			return
		}

		conf := config.ReceiverByName(data.Receiver)
		if conf == nil {
			errorHandler(w, http.StatusNotFound, fmt.Errorf("receiver missing: %s", data.Receiver), unknownReceiver, &data, logger)
			return
		}
		level.Debug(logger).Log("msg", "  matched receiver", "receiver", conf.Name)

		// TODO: Consider reusing notifiers or just jira clients to reuse connections.
		var client *jira.Client
		var err error
		if conf.User != "" && conf.Password != "" {
			tp := jira.BasicAuthTransport{
				Username: conf.User,
				Password: string(conf.Password),
			}
			client, err = jira.NewClient(tp.Client(), conf.APIURL)
		} else if conf.PersonalAccessToken != "" {
			tp := jira.PATAuthTransport{
				Token: string(conf.PersonalAccessToken),
			}
			client, err = jira.NewClient(tp.Client(), conf.APIURL)
		}

		if err != nil {
			errorHandler(w, http.StatusInternalServerError, err, conf.Name, &data, logger)
			return
		}

		if retry, err := notify.NewReceiver(logger, conf, tmpl, client.Issue).Notify(&data, fg.HashJiraLabel); err != nil {
			var status int
			if retry {
				// Instruct Alertmanager to retry.
				status = http.StatusServiceUnavailable
			} else {
				status = http.StatusInternalServerError
			}
			errorHandler(w, status, err, conf.Name, &data, logger)
			return
		}
		requestTotal.WithLabelValues(conf.Name, "200").Inc()

	})

	http.HandleFunc("/", HomeHandlerFunc())
	http.HandleFunc("/config", ConfigHandlerFunc(config))
	http.HandleFunc("/healthz", Healthcheck)
	http.HandleFunc("/actuator/*endpoint", Healthcheck)
	http.Handle("/metrics", promhttp.Handler())

	if os.Getenv("PORT") != "" {
		fg.ListenAddr = ":8080"
	}

	level.Info(logger).Log("msg", "listening", "address", fg.ListenAddr)
	err = http.ListenAndServe(fg.ListenAddr, nil)
	if err != nil {
		level.Error(logger).Log("msg", "failed to start HTTP server", "address", fg.ListenAddr)
		os.Exit(1)
	}
}

func errorHandler(w http.ResponseWriter, status int, err error, receiver string, data *alertmanager.Data, logger log.Logger) {
	w.WriteHeader(status)

	response := struct {
		Error   bool
		Status  int
		Message string
	}{
		true,
		status,
		err.Error(),
	}
	// JSON response
	bytes, _ := json.Marshal(response)
	json := string(bytes[:])
	fmt.Fprint(w, json)

	level.Error(logger).Log("msg", "error handling request", "statusCode", status, "statusText", http.StatusText(status), "err", err, "receiver", receiver, "groupLabels", data.GroupLabels)
	requestTotal.WithLabelValues(receiver, strconv.FormatInt(int64(status), 10)).Inc()
}

func setupLogger(lvl string, fmt string) (logger log.Logger) {
	var filter level.Option
	switch lvl {
	case "error":
		filter = level.AllowError()
	case "warn":
		filter = level.AllowWarn()
	case "debug":
		filter = level.AllowDebug()
	default:
		filter = level.AllowInfo()
	}

	if fmt == logFormatJSON {
		logger = log.NewJSONLogger(log.NewSyncWriter(os.Stderr))
	} else {
		logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	}
	logger = level.NewFilter(logger, filter)
	logger = log.With(logger, "ts", log.DefaultTimestampUTC, "caller", log.DefaultCaller)
	return
}

func Healthcheck(w http.ResponseWriter, r *http.Request) {
	versionBody, _ := os.ReadFile("git_commit")
	getactuator := actuator.GetActuatorHandler(&actuator.Config{
		Env:     "ft1",
		Name:    "jiralert",
		Port:    8080,
		Version: fmt.Sprintf("%s", versionBody),
	})
	getactuator(w, r)
	return
}
