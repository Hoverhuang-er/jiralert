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

package template

import (
	"bytes"
	"regexp"
	"strings"
	"text/template"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type Template struct {
	tmpl *template.Template
}

var funcs = template.FuncMap{
	"toUpper": strings.ToUpper,
	"toLower": strings.ToLower,
	"title":   strings.Title,
	// join is equal to strings.Join but inverts the argument order
	// for easier pipelining in templates.
	"join": func(sep string, s []string) string {
		return strings.Join(s, sep)
	},
	"match": regexp.MatchString,
	"reReplaceAll": func(pattern, repl, text string) string {
		re := regexp.MustCompile(pattern)
		return re.ReplaceAllString(text, repl)
	},
	"stringSlice": func(s ...string) []string {
		return s
	},
}

// LoadTemplate reads and parses all templates defined in the given file and constructs a jiralert.Template.
func LoadTemplate(path string) (*Template, error) {
	log.Debug("msg", "loading templates", "path", path)
	tmpl, err := template.New("").Option("missingkey=zero").Funcs(funcs).ParseFiles(path)
	if err != nil {
		return nil, err
	}
	return &Template{tmpl: tmpl}, nil
}

func SimpleTemplate() *Template {
	return &Template{tmpl: template.New("").Option("missingkey=zero").Funcs(funcs)}
}

// Execute parses the provided text (or returns it unchanged if not a Go template), associates it with the templates
// defined in t.tmpl (so they may be referenced and used) and applies the resulting template to the specified data
// object, returning the output as a string .
func (t *Template) Execute(text string, data interface{}) (string, error) {
	log.Debug("msg", "executing template", "template", text)
	if !strings.Contains(text, "{{") {
		log.Debug("msg", "returning unchanged")
		return text, nil
	}

	tmpl, err := t.tmpl.Clone()
	if err != nil {
		// There is literally no return flow in Clone that returns error.
		return "", errors.Wrap(err, "parse clone tmpl")
	}
	tmpl, err = tmpl.New("").Parse(text)
	if err != nil {
		return "", errors.Wrapf(err, "parse template %s", text)
	}
	var buf bytes.Buffer

	if err = tmpl.Execute(&buf, data); err != nil {
		return "", errors.Wrapf(err, "execute template %s", text)
	}
	ret := buf.String()
	log.Debug("msg", "template output", "output", ret)
	return ret, nil
}
