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
package notify

import (
	"bytes"
	"context"
	"crypto/sha512"
	"fmt"
	"io/ioutil"
	"reflect"
	"strings"
	"time"

	"github.com/Hoverhuang-er/jiralert/pkg/alertmanager"
	"github.com/Hoverhuang-er/jiralert/pkg/config"
	"github.com/Hoverhuang-er/jiralert/pkg/template"
	"github.com/andygrunwald/go-jira"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/trivago/tgo/tcontainer"
)

// TODO(bwplotka): Consider renaming this package to ticketer.

type jiraIssueService interface {
	Search(jql string, options *jira.SearchOptions) ([]jira.Issue, *jira.Response, error)
	GetTransitions(id string) ([]jira.Transition, *jira.Response, error)

	Create(issue *jira.Issue) (*jira.Issue, *jira.Response, error)
	UpdateWithOptions(issue *jira.Issue, opts *jira.UpdateQueryOptions) (*jira.Issue, *jira.Response, error)
	DoTransition(ticketID, transitionID string) (*jira.Response, error)
}

// Receiver wraps a specific Alertmanager receiver with its configuration and templates, creating/updating/reopening Jira issues based on Alertmanager notifications.
type Receiver struct {
	client jiraIssueService
	// TODO(bwplotka): Consider splitting receiver config with ticket service details.
	conf *config.ReceiverConfig
	tmpl *template.Template

	timeNow func() time.Time
}

// NewReceiver creates a Receiver using the provided configuration, template and jiraIssueService.
func NewReceiver(c *config.ReceiverConfig, t *template.Template, client jiraIssueService) *Receiver {
	return &Receiver{conf: c, tmpl: t, client: client, timeNow: time.Now}
}

// Notify manages JIRA issues based on alertmanager webhook notify message.
func (r *Receiver) Notify(ctx context.Context, data *alertmanager.Data, hashJiraLabel bool) (string, bool, error) {
	project, err := r.tmpl.Execute(r.conf.Project, data)
	if err != nil {
		log.Errorf("failed to execute project template", "err", err)
		return "", false, errors.Wrap(err, "generate project from template")
	}
	issueGroupLabel := toGroupTicketLabel(ctx, data.GroupLabels, hashJiraLabel)
	issue, retry, err := r.findIssueToReuse(ctx, project, issueGroupLabel)
	if err != nil {
		log.Errorf("failed to find issue to reuse", "err", err)
		return "", retry, err
	}
	// We want up to date title no matter what.
	// This allows reflecting current group state if desired by user e.g {{ len $.Alerts.Firing() }}
	issueSummary, err := r.tmpl.Execute(r.conf.Summary, data)
	if err != nil {
		log.Errorf("failed to execute summary template", "err", err)
		return "", false, errors.Wrap(err, "generate summary from template")
	}
	log.Infof("issue summary", "summary", issueSummary)
	issueDesc, err := r.tmpl.Execute(r.conf.Description, data)
	if err != nil {
		log.Errorf("failed to execute description template", "err", err)
		return "", false, errors.Wrap(err, "render issue description")
	}
	log.Info(issue)
	if issue != nil {
		// Update summary if needed.
		if issue.Fields.Summary != issueSummary {
			retry, err := r.updateSummary(issue.Key, issueSummary)
			if err != nil {
				log.Errorf("failed to update summary", "err", err)
				return "", retry, err
			}
		}
		log.Debug("msg", "found issue to reuse", "issue", issue.Key)
		if issue.Fields.Description != issueDesc {
			retry, err := r.updateDescription(issue.Key, issueDesc)
			if err != nil {
				log.Errorf("failed to update description", "err", err)
				return "", retry, err
			}
		}
		log.Debug("msg", "issue found, reusing", "key", issue.Key, "id", issue.ID)
		if cap(data.Alerts.Firing()) == 0 {
			if r.conf.AutoResolve != nil {
				log.Debug("msg", "no firing alert; resolving issue", "key", issue.Key, "label", issueGroupLabel)
				retry, err := r.resolveIssue(issue.Key)
				if err != nil {
					log.Errorf("failed to resolve issue", "err", err)
					return "", retry, err
				}
				log.Warningf("issue resolved", "key", issue.Key)
				return "", false, nil
			}
			log.Debug("msg", "no firing alert; summary checked, nothing else to do.", "key", issue.Key, "label", issueGroupLabel)
			return "", false, nil
		}
		log.Debug("msg", "issue found; summary checked, nothing else to do.", "key", issue.Key, "label", issueGroupLabel)
		// The set of JIRA status categories is fixed, this is a safe check to make.
		if issue.Fields.Status.StatusCategory.Key != "done" {
			log.Debug("msg", "issue is unresolved, all is done", "key", issue.Key, "label", issueGroupLabel)
			return "", false, nil
		}
		log.Debug("msg", "issue is resolved, reopening", "key", issue.Key, "label", issueGroupLabel)
		if r.conf.WontFixResolution != "" && issue.Fields.Resolution != nil &&
			issue.Fields.Resolution.Name == r.conf.WontFixResolution {
			log.Info("msg", "issue was resolved as won't fix, not reopening", "key", issue.Key, "label", issueGroupLabel, "resolution", issue.Fields.Resolution.Name)
			return "", false, nil
		}
		log.Debug("msg", "issue is resolved, reopening", "key", issue.Key, "label", issueGroupLabel)
		b, err := r.reopen(issue.Key)
		log.Info("msg", "issue was recently resolved, reopening", "key", issue.Key, "label", issueGroupLabel)
		return "", b, err
	}
	if cap(data.Alerts.Firing()) == 0 {
		log.Debugf("no firing alert; nothing to do.label:%s", issueGroupLabel)
	} else {
		log.Warnf("no issue found, creating a new one label:%s", issueGroupLabel)
	}
	issueType, err := r.tmpl.Execute(r.conf.IssueType, data)
	if err != nil {
		return "", false, errors.Wrap(err, "render issue type")
	}
	issue = &jira.Issue{
		Fields: &jira.IssueFields{
			Project:     jira.Project{Key: project},
			Type:        jira.IssueType{Name: issueType},
			Description: issueDesc,
			Summary:     issueSummary,
			Labels:      []string{issueGroupLabel},
			Unknowns:    tcontainer.NewMarshalMap(),
		},
	}
	if r.conf.Priority != "" {
		issuePrio, err := r.tmpl.Execute(r.conf.Priority, data)
		if err != nil {
			return "", false, errors.Wrap(err, "render issue priority")
		}

		issue.Fields.Priority = &jira.Priority{Name: issuePrio}
	}
	if cap(r.conf.Components) > 0 {
		issue.Fields.Components = make([]*jira.Component, 0, len(r.conf.Components))
		for _, component := range r.conf.Components {
			issueComp, err := r.tmpl.Execute(component, data)
			if err != nil {
				return "", false, errors.Wrap(err, "render issue component")
			}
			issue.Fields.Components = append(issue.Fields.Components, &jira.Component{Name: issueComp})
		}
	}
	if r.conf.AddGroupLabels {
		for k, v := range data.GroupLabels {
			issue.Fields.Labels = append(issue.Fields.Labels, fmt.Sprintf("%s=%q", k, v))
		}
	}
	for key, value := range r.conf.Fields {
		issue.Fields.Unknowns[key], err = deepCopyWithTemplate(ctx, value, r.tmpl, data)
		if err != nil {
			return "", false, err
		}
	}
	b, err := r.create(issue)
	return issue.Key, b, nil
}

// deepCopyWithTemplate returns a deep copy of a map/slice/array/string/int/bool or combination thereof, executing the
// provided template (with the provided data) on all string keys or values. All maps are connverted to
// map[string]interface{}, with all non-string keys discarded.
func deepCopyWithTemplate(ctx context.Context, value interface{}, tmpl *template.Template, data interface{}) (interface{}, error) {
	if value == nil {
		return value, nil
	}

	valueMeta := reflect.ValueOf(value)
	switch valueMeta.Kind() {

	case reflect.String:
		return tmpl.Execute(value.(string), data)

	case reflect.Array, reflect.Slice:
		arrayLen := valueMeta.Len()
		converted := make([]interface{}, arrayLen)
		for i := 0; i < arrayLen; i++ {
			var err error
			converted[i], err = deepCopyWithTemplate(ctx, valueMeta.Index(i).Interface(), tmpl, data)
			if err != nil {
				return nil, err
			}
		}
		return converted, nil

	case reflect.Map:
		keys := valueMeta.MapKeys()
		converted := make(map[string]interface{}, len(keys))

		for _, keyMeta := range keys {
			var err error
			strKey, isString := keyMeta.Interface().(string)
			if !isString {
				continue
			}
			strKey, err = tmpl.Execute(strKey, data)
			if err != nil {
				return nil, err
			}
			converted[strKey], err = deepCopyWithTemplate(ctx, valueMeta.MapIndex(keyMeta).Interface(), tmpl, data)
			if err != nil {
				return nil, err
			}
		}
		return converted, nil
	default:
		return value, nil
	}
}

// toGroupTicketLabel returns the group labels as a single string.
// This is used to reference each ticket groups.
// (old) default behavior: String is the form of an ALERT Prometheus metric name, with all spaces removed.
// new opt-in behavior: String is the form of JIRALERT{sha512hash(groupLabels)}
// hashing ensures that JIRA validation still accepts the output even
// if the combined length of all groupLabel key-value pairs would be
// longer than 255 chars
func toGroupTicketLabel(ctx context.Context, groupLabels alertmanager.KV, hashJiraLabel bool) string {
	// new opt in behavior
	if hashJiraLabel {
		hash := sha512.New()
		for _, p := range groupLabels.SortedPairs() {
			kvString := fmt.Sprintf("%s:%q,", p.Name, p.Value)
			_, _ = hash.Write([]byte(kvString)) // hash.Write can never return an error
		}
		return fmt.Sprintf("JIRALERT{%x}", hash.Sum(nil))
	}

	// old default behavior
	buf := bytes.NewBufferString("ALERT{")
	for _, p := range groupLabels.SortedPairs() {
		buf.WriteString(p.Name)
		buf.WriteString(fmt.Sprintf("=%q,", p.Value))
	}
	buf.Truncate(buf.Len() - 1)
	buf.WriteString("}")
	return strings.Replace(buf.String(), " ", "", -1)
}

func (r *Receiver) search(ctx context.Context, project, issueLabel string) (*jira.Issue, bool, error) {
	query := fmt.Sprintf("project=\"%s\" and labels=%q order by resolutiondate desc", project, issueLabel)
	options := &jira.SearchOptions{
		Fields:     []string{"summary", "status", "resolution", "resolutiondate"},
		MaxResults: 2,
	}

	log.Debug("msg", "search", "query", query, "options", fmt.Sprintf("%+v", options))
	issues, resp, err := r.client.Search(query, options)
	if err != nil {
		retry, err := handleJiraErrResponse("Issue.Search", resp, err)
		return nil, retry, err
	}

	if cap(issues) == 0 {
		log.Debug("msg", "no results", "query", query)
		return nil, false, nil
	}

	issue := issues[0]
	if len(issues) > 1 {
		log.Warn("msg", "more than one issue matched, picking most recently resolved", "query", query, "issues", issues, "picked", issue)
	}

	log.Debug("msg", "found", "issue", issue, "query", query)
	return &issue, false, nil
}

func (r *Receiver) findIssueToReuse(ctx context.Context, project string, issueGroupLabel string) (*jira.Issue, bool, error) {
	issue, retry, err := r.search(ctx, project, issueGroupLabel)
	if err != nil {
		return nil, retry, err
	}

	if issue == nil {
		return nil, false, nil
	}

	resolutionTime := time.Time(issue.Fields.Resolutiondate)
	if resolutionTime != (time.Time{}) && resolutionTime.Add(time.Duration(*r.conf.ReopenDuration)).Before(r.timeNow()) && *r.conf.ReopenDuration != 0 {
		log.Debug("msg", "existing resolved issue is too old to reopen, skipping", "key", issue.Key, "label", issueGroupLabel, "resolution_time", resolutionTime.Format(time.RFC3339), "reopen_duration", *r.conf.ReopenDuration)
		return nil, false, nil
	}

	// Reuse issue.
	return issue, false, nil
}

func (r *Receiver) updateSummary(issueKey string, summary string) (bool, error) {
	log.Debug("msg", "updating issue with new summary", "key", issueKey, "summary", summary)

	issueUpdate := &jira.Issue{
		Key: issueKey,
		Fields: &jira.IssueFields{
			Summary: summary,
		},
	}
	issue, resp, err := r.client.UpdateWithOptions(issueUpdate, nil)
	if err != nil {
		return handleJiraErrResponse("Issue.UpdateWithOptions", resp, err)
	}
	log.Debug("msg", "issue summary updated", "key", issue.Key, "id", issue.ID)
	return false, nil
}

func (r *Receiver) updateDescription(issueKey string, description string) (bool, error) {
	log.Debug("msg", "updating issue with new description", "key", issueKey, "description", description)

	issueUpdate := &jira.Issue{
		Key: issueKey,
		Fields: &jira.IssueFields{
			Description: description,
		},
	}
	issue, resp, err := r.client.UpdateWithOptions(issueUpdate, nil)
	if err != nil {
		return handleJiraErrResponse("Issue.UpdateWithOptions", resp, err)
	}
	log.Debug("msg", "issue summary updated", "key", issue.Key, "id", issue.ID)
	return false, nil
}

func (r *Receiver) reopen(issueKey string) (bool, error) {
	return r.doTransition(issueKey, r.conf.ReopenState)
}

func (r *Receiver) create(issue *jira.Issue) (bool, error) {
	log.Debug("msg", "create", "issue", fmt.Sprintf("%+v", *issue.Fields))
	newIssue, resp, err := r.client.Create(issue)
	if err != nil {
		return handleJiraErrResponse("Issue.Create", resp, err)
	}
	*issue = *newIssue

	log.Info("msg", "issue created", "key", issue.Key, "id", issue.ID)
	return false, nil
}

func handleJiraErrResponse(api string, resp *jira.Response, err error) (bool, error) {
	if resp == nil || resp.Request == nil {
		log.Debug("msg", "handleJiraErrResponse", "api", api, "err", err)
	} else {
		log.Debug("msg", "handleJiraErrResponse", "api", api, "err", err, "url", resp.Request.URL)
	}

	if resp != nil && resp.StatusCode/100 != 2 {
		retry := resp.StatusCode == 500 || resp.StatusCode == 503
		body, _ := ioutil.ReadAll(resp.Body)
		// go-jira error message is not particularly helpful, replace it
		return retry, errors.Errorf("JIRA request %s returned status %s, body %q", resp.Request.URL, resp.Status, string(body))
	}
	return false, errors.Wrapf(err, "JIRA request %s failed", api)
}

func (r *Receiver) resolveIssue(issueKey string) (bool, error) {
	return r.doTransition(issueKey, r.conf.AutoResolve.State)
}

func (r *Receiver) doTransition(issueKey string, transitionState string) (bool, error) {
	transitions, resp, err := r.client.GetTransitions(issueKey)
	if err != nil {
		return handleJiraErrResponse("Issue.GetTransitions", resp, err)
	}

	for _, t := range transitions {
		if t.Name == transitionState {
			log.Debug("msg", fmt.Sprintf("transition %s", transitionState), "key", issueKey, "transitionID", t.ID)
			resp, err = r.client.DoTransition(issueKey, t.ID)
			if err != nil {
				return handleJiraErrResponse("Issue.DoTransition", resp, err)
			}

			log.Debug("msg", transitionState, "key", issueKey)
			return false, nil
		}
	}
	return false, errors.Errorf("JIRA state %q does not exist or no transition possible for %s", r.conf.ReopenState, issueKey)

}
