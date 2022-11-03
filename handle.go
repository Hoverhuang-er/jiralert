package main

import (
	"context"
	"encoding/json"
	"github.com/Hoverhuang-er/jiralert/pkg/alertmanager"
	"github.com/Hoverhuang-er/jiralert/pkg/config"
	"github.com/Hoverhuang-er/jiralert/pkg/notify"
	"github.com/Hoverhuang-er/jiralert/pkg/template"
	"github.com/andygrunwald/go-jira"
	jsoniter "github.com/json-iterator/go"
	"github.com/pkg/errors"
	"io"
	"net/http"
)

type Jiralert struct {
	Input       io.Reader
	Config      *config.Config
	Template    *template.Template
	IsHashLable bool
}

func (je Jiralert) NewIssues(ctx context.Context) (string, error) {
	data := alertmanager.Data{}
	body, err := io.ReadAll(je.Input)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}
	conf := je.Config.ReceiverByName(ctx, data.Receiver)
	if conf == nil {
		return "", err
	}
	tp := jira.BasicAuthTransport{
		Username: conf.User,
		Password: string(conf.Password),
	}
	client, err := jira.NewClient(tp.Client(), conf.APIURL)
	if err != nil {
		return "", err
	}
	key, retry, err := notify.NewReceiver(conf, je.Template, client.Issue).Notify(ctx, &data, je.IsHashLable)
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
