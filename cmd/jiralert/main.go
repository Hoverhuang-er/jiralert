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
	"context"
	"encoding/json"
	"fmt"
	"github.com/Hoverhuang-er/go-actuator"
	"github.com/Hoverhuang-er/jiralert"
	"github.com/Hoverhuang-er/jiralert/pkg/alertmanager"
	"github.com/Hoverhuang-er/jiralert/pkg/config"
	"github.com/Hoverhuang-er/jiralert/pkg/notify"
	"github.com/Hoverhuang-er/jiralert/pkg/template"
	"github.com/andygrunwald/go-jira"
	jsoniter "github.com/json-iterator/go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"gocloud.dev/server"
	"gocloud.dev/server/requestlog"
	"io"
	"net/http"
	"os"
	"runtime"
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
	ncu := runtime.NumCPU()
	runtime.GOMAXPROCS(ncu)
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
	srv := server.New(http.DefaultServeMux, &server.Options{
		RequestLogger: requestlog.NewNCSALogger(os.Stdout, func(error) {}),
	})
	http.HandleFunc("/alert/deprecated", func(w http.ResponseWriter, req *http.Request) {
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
		wb, _ := jsoniter.MarshalToString(map[string]interface{}{
			"code":      http.StatusOK,
			"msg":       "success",
			"issue_key": key,
		})
		w.Write([]byte(wb))
		w.WriteHeader(http.StatusOK)
		config.RequestTotal.WithLabelValues(conf.Name, "200").Inc()
		return
	})
	http.HandleFunc("/alert", func(writer http.ResponseWriter, request *http.Request) {
		var data alertmanager.Data
		ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
		defer func() {
			_ = request.Body.Close()
			cancel()
		}()
		if err := jsoniter.NewDecoder(request.Body).Decode(&data); err != nil {
			log.Errorf("msg", "failed to parse request body: %v", err)
			return
		}
		je := jiralert.Jiralert{
			Input:       &data,
			Config:      config2,
			Template:    tmpl,
			IsHashLable: false,
		}
		resp, err := je.NewIssues(ctx)
		if err != nil {
			log.Errorf("msg", "failed to create jira issue: %v", err)
			return
		}
		wb, _ := jsoniter.MarshalToString(resp)
		writer.Write([]byte(wb))
		writer.WriteHeader(http.StatusOK)
		return
	})
	http.HandleFunc("/", jiralert.HomeHandlerFunc())
	http.HandleFunc("/config", jiralert.ConfigHandlerFunc(config2))
	http.HandleFunc("/healthz", Healthcheck)
	http.HandleFunc("/actuator/*endpoint", Healthcheck)
	http.Handle("/metrics", promhttp.Handler())
	if os.Getenv("PORT") != "" {
		fg.ListenAddr = ":8080"
	}
	log.Warn("listening", "address", fg.ListenAddr)
	if srv.ListenAndServe(fg.ListenAddr) != http.ErrServerClosed {
		log.Fatal("msg", "server exited abnormally")
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

func HealthCheckFunc() error {
	return nil
}
