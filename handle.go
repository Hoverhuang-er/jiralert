package main

import (
	"context"
	"github.com/Hoverhuang-er/jiralert/pkg/alertmanager"
	"github.com/Hoverhuang-er/jiralert/pkg/config"
	"github.com/Hoverhuang-er/jiralert/pkg/notify"
	"github.com/Hoverhuang-er/jiralert/pkg/template"
	"github.com/andygrunwald/go-jira"
	jsoniter "github.com/json-iterator/go"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"net/http"
	"os"
)

var defaultTemplate = `
{{ define "jira.summary" }}[{{ .Status | toUpper }}{{ if eq .Status "firing" }}:{{ .Alerts.Firing | len }}{{ end }}] {{ .GroupLabels.SortedPairs.Values | join " " }} {{ if gt (len .CommonLabels) (len .GroupLabels) }}({{ with .CommonLabels.Remove .GroupLabels.Names }}{{ .Values | join " " }}{{ end }}){{ end }}{{ end }}

{{ define "jira.description" }}{{ range .Alerts.Firing }}Labels:
{{ range .Labels.SortedPairs }} - {{ .Name }} = {{ .Value }}
{{ end }}
Annotations:
{{ range .Annotations.SortedPairs }} - {{ .Name }} = {{ .Value }}
{{ end }}
Source: {{ .GeneratorURL }}
{{ end }}{{ end }}
`

type Jiralert struct {
	Input       *alertmanager.Data
	Config      *config.Config
	Template    *template.Template
	IsHashLable bool
}
type JiralertFunc interface {
	NewIssues(ctx context.Context) (string, error)
	UpdateIssues(ctx context.Context) (string, error)
	CloseIssues(ctx context.Context) (string, error)
}

// New Issues a new Jiralert.
func (je Jiralert) NewIssues(ctx context.Context) (string, error) {
	conf, err := CheckConfig(ctx, je.Config)
	if err != nil {
		return "", errors.Wrap(err, "check config failed")
	}
	if err := checkTemplate(ctx); err != nil {
		return "", errors.Wrap(err, "failed to check template")
	}
	conf2 := conf.ReceiverByName(ctx, conf.Receivers[0].Name)
	tp := jira.BasicAuthTransport{
		Username: conf2.User,
		Password: string(conf2.Password),
	}
	client, err := jira.NewClient(tp.Client(), conf2.APIURL)
	if err != nil {
		return "", err
	}
	key, retry, err := notify.NewReceiver(conf2, je.Template, client.Issue).Notify(ctx, je.Input, je.IsHashLable)
	if err != nil {
		if retry {
			return "", errors.New("retry")
		}
		return "", err
	}
	return jsoniter.MarshalToString(map[string]interface{}{
		"code":      http.StatusOK,
		"msg":       "success",
		"issue_key": key,
	})
}

// Verify Config if not exist
func CheckConfig(ctx context.Context, je *config.Config) (*config.Config, error) {
	dfc := &config.ReceiverConfig{
		Name:              "test",
		APIURL:            "http://simple-labs.rest:8080",
		User:              "admin",
		Password:          "admin",
		Project:           "SRE",
		IssueType:         "Task",
		Priority:          "Medium",
		Description:       "This is a test issue",
		WontFixResolution: "Won't Fix",
		AddGroupLabels:    false,
	}
	if je != nil {
		log.Info("config is exist")
		return je, nil
	}
	return &config.Config{
		Defaults: dfc,
		Receivers: []*config.ReceiverConfig{
			dfc,
		},
		Template: "jiralert.tmpl",
	}, nil
}

// Verify Template if not exist
func checkTemplate(ctx context.Context) error {
	_, err := template.LoadTemplate("jiralert.tmpl")
	if err != nil {
		ft, err := os.OpenFile("jiralert.tmpl", os.O_RDWR|os.O_CREATE, 0755)
		if os.IsNotExist(err) {
			ft.WriteString(defaultTemplate)
			ft.Close()
			log.Info("create template file jiralert.tmpl")
			return checkTemplate(ctx)
		} else if os.IsExist(err) {
			os.Remove("jiralert.tmpl")
			log.Errorf("create template file jiralert.tmpl failed: %v", err)
			ft.WriteString(defaultTemplate)
			ft.Close()
			return checkTemplate(ctx)
		}
	}
	log.Infof("template file jiralert.tmpl is exist")
	return nil
}
