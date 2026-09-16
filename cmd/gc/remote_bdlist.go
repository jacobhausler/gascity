package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/api"
)

// maybeRouteRemoteBdList serves the read-only `gc bd list` subset from the
// existing remote city API client. handled is false for every local path so
// doBd retains its current resolution, split-store, and bd passthrough
// behavior byte-for-byte.
func maybeRouteRemoteBdList(cityName, rigName string, bdArgs []string, stdout, stderr io.Writer) (code int, handled bool) {
	if len(bdArgs) == 0 || bdArgs[0] != "list" || cityName != "" || rigName != "" {
		return 0, false
	}

	ctx, err := resolveContextAllowRemote()
	if err != nil || ctx.Remote == nil {
		return 0, false
	}
	client, err := buildRemoteClient(ctx.Remote)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd list: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1, true
	}
	options, err := parseRemoteBdListArgs(bdArgs, rigName)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd list: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1, true
	}

	result, err := client.ListBeads(options.query)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd list: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1, true
	}
	sortBeadsForList(result.Body)
	if options.format == "json" {
		writeBeadsJSON(result.Body, stdout)
	} else {
		writeBeadTable(result.Body, stdout, true)
	}
	return 0, true
}

type remoteBdListOptions struct {
	query  api.ListBeadsOpts
	format string
}

// parseRemoteBdListArgs maps the selector subset understood by the city beads
// API. Unsupported bd list flags fail closed rather than being silently
// dropped and returning a different set of beads than the caller requested.
func parseRemoteBdListArgs(args []string, rigName string) (remoteBdListOptions, error) {
	if len(args) == 0 || args[0] != "list" {
		return remoteBdListOptions{}, fmt.Errorf("internal error: expected bd list arguments")
	}
	options := remoteBdListOptions{
		query:  api.ListBeadsOpts{Rig: rigName},
		format: "text",
	}

	readValue := func(args []string, index *int, flag string) (string, error) {
		if *index+1 >= len(args) {
			return "", fmt.Errorf("%s requires a value", flag)
		}
		(*index)++
		return args[*index], nil
	}
	readFlagValue := func(args []string, index *int, long, short string) (value string, matched bool, err error) {
		arg := args[*index]
		switch {
		case arg == long || arg == short:
			value, err = readValue(args, index, arg)
			return value, true, err
		case strings.HasPrefix(arg, long+"="):
			return strings.TrimPrefix(arg, long+"="), true, nil
		case short != "" && strings.HasPrefix(arg, short+"="):
			return strings.TrimPrefix(arg, short+"="), true, nil
		default:
			return "", false, nil
		}
	}

	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--json":
			options.format = "json"
		case "--all":
			options.query.All = true
		default:
			if value, matched, err := readFlagValue(args, &i, "--format", ""); matched {
				if err != nil {
					return remoteBdListOptions{}, err
				}
				if value != "text" && value != "json" {
					return remoteBdListOptions{}, fmt.Errorf("unsupported remote output format %q", value)
				}
				options.format = value
				continue
			}
			if value, matched, err := readFlagValue(args, &i, "--status", "-s"); matched {
				if err != nil {
					return remoteBdListOptions{}, err
				}
				options.query.Status = value
				continue
			}
			if value, matched, err := readFlagValue(args, &i, "--type", "-t"); matched {
				if err != nil {
					return remoteBdListOptions{}, err
				}
				options.query.Type = value
				continue
			}
			if value, matched, err := readFlagValue(args, &i, "--label", "-l"); matched {
				if err != nil {
					return remoteBdListOptions{}, err
				}
				options.query.Label = value
				continue
			}
			if value, matched, err := readFlagValue(args, &i, "--assignee", "-a"); matched {
				if err != nil {
					return remoteBdListOptions{}, err
				}
				options.query.Assignee = value
				continue
			}
			if value, matched, err := readFlagValue(args, &i, "--limit", "-n"); matched {
				if err != nil {
					return remoteBdListOptions{}, err
				}
				limit, parseErr := strconv.Atoi(value)
				if parseErr != nil || limit < 0 {
					return remoteBdListOptions{}, fmt.Errorf("invalid --limit %q: want a non-negative integer", value)
				}
				options.query.Limit = limit
				continue
			}
			return remoteBdListOptions{}, fmt.Errorf("flag or argument %q is not supported for a remote city", arg)
		}
	}
	return options, nil
}
