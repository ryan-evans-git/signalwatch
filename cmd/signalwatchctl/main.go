// signalwatchctl is a small client CLI for the signalwatch HTTP API. It
// covers list/show/delete operations and accepts JSON on stdin for create
// and update.
//
// Usage:
//
//   signalwatchctl rules list
//   signalwatchctl rules show <id>
//   signalwatchctl rules create < rule.json
//   signalwatchctl rules delete <id>
//   signalwatchctl subscribers list
//   signalwatchctl subscriptions list
//   signalwatchctl incidents list
//   signalwatchctl notifications list
//   signalwatchctl states
//   signalwatchctl emit '{"input_ref":"events","record":{"level":"ERROR"}}'
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func main() {
	addr := flag.String("addr", "http://127.0.0.1:8080", "service address")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	if err := dispatch(*addr, args); err != nil {
		fmt.Fprintln(os.Stderr, "signalwatchctl:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: signalwatchctl [--addr URL] <resource> <verb> [args]

resources:
  rules           list | show <id> | create | update <id> | delete <id>
  subscribers     list | show <id> | create | update <id> | delete <id>
  subscriptions   list | show <id> | create | update <id> | delete <id>
  incidents       list | show <id>
  notifications   list
  states          (no verb)
  emit            <json-payload>   -- POST to /v1/events`)
}

func dispatch(addr string, args []string) error {
	resource := args[0]
	switch resource {
	case "rules":
		return crud(addr, "/v1/rules", args[1:])
	case "subscribers":
		return crud(addr, "/v1/subscribers", args[1:])
	case "subscriptions":
		return crud(addr, "/v1/subscriptions", args[1:])
	case "incidents":
		if len(args) < 2 {
			return doGet(addr, "/v1/incidents")
		}
		switch args[1] {
		case "list":
			return doGet(addr, "/v1/incidents")
		case "show":
			if len(args) < 3 {
				return fmt.Errorf("incidents show: missing id")
			}
			return doGet(addr, "/v1/incidents/"+args[2])
		}
	case "notifications":
		return doGet(addr, "/v1/notifications")
	case "states":
		return doGet(addr, "/v1/states")
	case "emit":
		if len(args) < 2 {
			return fmt.Errorf("emit: missing JSON payload")
		}
		return doPost(addr, "/v1/events", []byte(args[1]))
	default:
		usage()
		return fmt.Errorf("unknown resource %q", resource)
	}
	return nil
}

func crud(addr, base string, args []string) error {
	if len(args) == 0 {
		return doGet(addr, base)
	}
	switch args[0] {
	case "list":
		return doGet(addr, base)
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("show: missing id")
		}
		return doGet(addr, base+"/"+args[1])
	case "create":
		body, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		return doPost(addr, base, body)
	case "update":
		if len(args) < 2 {
			return fmt.Errorf("update: missing id")
		}
		body, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		return doPut(addr, base+"/"+args[1], body)
	case "delete":
		if len(args) < 2 {
			return fmt.Errorf("delete: missing id")
		}
		return doDelete(addr, base+"/"+args[1])
	default:
		return fmt.Errorf("unknown verb %q", args[0])
	}
}

func doGet(addr, path string) error    { return doReq(http.MethodGet, addr+path, nil) }
func doPost(addr, path string, b []byte) error  { return doReq(http.MethodPost, addr+path, b) }
func doPut(addr, path string, b []byte) error   { return doReq(http.MethodPut, addr+path, b) }
func doDelete(addr, path string) error { return doReq(http.MethodDelete, addr+path, nil) }

func doReq(method, url string, body []byte) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %d %s", method, url, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	if len(out) == 0 {
		return nil
	}
	// Pretty-print JSON if possible.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out, "", "  "); err == nil {
		fmt.Println(pretty.String())
	} else {
		fmt.Println(string(out))
	}
	return nil
}
