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
	"github.com/Hoverhuang-er/jiralert/pkg/alertmanager"
	"github.com/Hoverhuang-er/jiralert/pkg/config"
	"github.com/Hoverhuang-er/jiralert/pkg/notify"
	"github.com/Hoverhuang-er/jiralert/pkg/template"
	"github.com/andygrunwald/go-jira"
	klog "github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	jsoniter "github.com/json-iterator/go"
	atmpl "github.com/prometheus/alertmanager/template"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"io"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"runtime/debug"
	"time"
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
	ncu := runtime.NumCPU()
	runtime.GOMAXPROCS(ncu)
	debug.ReadBuildInfo()
	debug.PrintStack()
	fg := Flg{
		Loglevel:      "info",
		Logfmt:        logFormatLogfmt,
		Config:        "./jiralert.yml",
		ListenAddr:    ":8080",
		HashJiraLabel: false,
		Version:       "<local build>",
	}
	if !fg.HashJiraLabel {
		log.Warn("msg", "Using deprecated jira label generation - "+
			"please read https://github.com/prometheus-community/jiralert/pull/79 "+
			"and try -hash-jira-label")
	}
	config2, _, err := config.LoadFile(fg.Config)
	if err != nil {
		log.Errorf("msg", "loading configuration path%s err%v", fg.Config, err)
		os.Exit(1)
	}
	tmpl, err := template.LoadTemplate(config2.Template)
	if err != nil {
		log.Error("msg", "loading templates", "path", config2.Template, "err", err)
		return
	}
	http.HandleFunc("/alert", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
		defer func() {
			_ = req.Body.Close()
			cancel()
		}()
		// https://godoc.org/github.com/prometheus/alertmanager/template#Data
		data := alertmanager.Data{}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			errorHandler(w, http.StatusBadRequest, fmt.Errorf("failed to read request body: %v", err))
			return
		}
		if err := json.Unmarshal(body, &data); err != nil {
			errorHandler(w, http.StatusBadRequest, fmt.Errorf("failed to parse request body: %v", err))
			return
		}
		conf := config2.ReceiverByName(ctx, data.Receiver)
		if conf == nil {
			log.Errorf("config not found", conf)
			errorHandler(w, http.StatusOK, fmt.Errorf("receiver missing: %s", data.Receiver))
			return
		}
		// TODO: Consider reusing notifiers or just jira clients to reuse connections.
		tp := jira.BasicAuthTransport{
			Username: conf.User,
			Password: string(conf.Password),
		}
		client, err := jira.NewClient(tp.Client(), conf.APIURL)
		if err != nil {
			log.Errorf("Failed to create Jira client: %v", err)
			errorHandler(w, http.StatusOK, err)
			return
		}
		var status int
		key, retry, err := notify.NewReceiver(conf, tmpl, client.Issue).Notify(ctx, &data, fg.HashJiraLabel)
		if err != nil {
			if retry {
				status = http.StatusServiceUnavailable
			}
			log.Errorf("send notify error:%s", err.Error())
			errorHandler(w, status, err)
			return
		}
		requestTotal.WithLabelValues(conf.Name, "200").Inc()
		wb, _ := jsoniter.MarshalToString(map[string]interface{}{
			"code":      http.StatusOK,
			"msg":       "success",
			"issue_key": key,
		})
		w.Write([]byte(wb))
		w.WriteHeader(http.StatusOK)
		return
	})
	http.Handle("/logger", &handler{})
	http.HandleFunc("/", HomeHandlerFunc())
	http.HandleFunc("/config", ConfigHandlerFunc(config2))
	http.HandleFunc("/healthz", Healthcheck)
	http.HandleFunc("/actuator/*endpoint", Healthcheck)
	http.Handle("/metrics", promhttp.Handler())
	if os.Getenv("PORT") != "" {
		fg.ListenAddr = ":8080"
	}
	log.Warn("listening", "address", fg.ListenAddr)
	err = http.ListenAndServe(fg.ListenAddr, nil)
	if err != nil {
		log.Error("msg", "failed to start HTTP server", "address", fg.ListenAddr)
		os.Exit(1)
	}
}

func errorHandler(w http.ResponseWriter, status int, err error) {
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
	log.Error("msg", "error handling request", "statusCode", status, "statusText", http.StatusText(status),
		"err", err)
}

func setupLogger(lvl string, fmt string) (logger klog.Logger) {
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
		logger = klog.NewJSONLogger(klog.NewSyncWriter(os.Stderr))
	} else {
		logger = klog.NewLogfmtLogger(klog.NewSyncWriter(os.Stderr))
	}
	logger = level.NewFilter(logger, filter)
	logger = klog.With(logger, "ts", klog.DefaultTimestampUTC, "caller", klog.DefaultCaller)
	return
}

func Healthcheck(w http.ResponseWriter, r *http.Request) {
	versionBody, _ := os.ReadFile("git_commit")
	getactuator := actuator.GetActuatorHandler(&actuator.Config{
		Env:     "debug",
		Name:    "jiralert",
		Port:    8080,
		Version: fmt.Sprintf("%s", versionBody),
	})
	getactuator(w, r)
	return
}

func logWith(values map[string]string, logger klog.Logger) klog.Logger {
	for k, v := range values {
		logger = klog.With(logger, k, v)
	}
	return logger
}
func logAlerts(alerts atmpl.Data, logger klog.Logger) error {
	logger = logWith(alerts.CommonAnnotations, logger)
	logger = logWith(alerts.CommonLabels, logger)
	logger = logWith(alerts.GroupLabels, logger)
	for _, alert := range alerts.Alerts {
		alertLogger := logWith(alert.Labels, logger)
		alertLogger = logWith(alert.Annotations, alertLogger)
		if err := alertLogger.Log("status", alert.Status, "startsAt", alert.StartsAt, "endsAt", alert.EndsAt,
			"generatorURL", alert.GeneratorURL, "externalURL", alerts.ExternalURL, "receiver", alerts.Receiver,
			"fingerprint", alert.Fingerprint); err != nil {
			return err
		}
		log.Infof("OUTPUT:%v", alertLogger)
	}
	return nil
}

type handler struct {
	Logger klog.Logger
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var alerts atmpl.Data
	err := json.NewDecoder(r.Body).Decode(&alerts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := logAlerts(alerts, h.Logger); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	return
}
