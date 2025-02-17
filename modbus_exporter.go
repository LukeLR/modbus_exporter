// Copyright 2019 Richard Hartmann
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
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/alecthomas/kingpin/v2"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	webflag "github.com/prometheus/exporter-toolkit/web/kingpinflag"

	"github.com/RichiH/modbus_exporter/config"
	"github.com/RichiH/modbus_exporter/modbus"
)

func main() {
	var (
		configFile = kingpin.Flag(
			"config.file",
			"Sets the configuration file.",
		).Default("modbus.yml").String()
		toolkitFlags = webflag.AddFlags(kingpin.CommandLine, ":9602")
	)

	promlogConfig := &promlog.Config{}
	flag.AddFlags(kingpin.CommandLine, promlogConfig)
	kingpin.Version(version.Print("modbus_exporter"))
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promlog.New(promlogConfig)

	level.Info(logger).Log("msg", "Starting modbus_exporter", "version", version.Info())
	level.Info(logger).Log("build_context", version.BuildContext())

	telemetryRegistry := prometheus.NewRegistry()
	telemetryRegistry.MustRegister(collectors.NewGoCollector())
	telemetryRegistry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	level.Info(logger).Log("msg", "Loading configuration file", "config_file", *configFile)
	config, err := config.LoadConfig(*configFile)
	if err != nil {
		level.Error(logger).Log("msg", "Error loading config", "err", err)
		os.Exit(1)
	}

	http.Handle("/metrics", promhttp.HandlerFor(telemetryRegistry, promhttp.HandlerOpts{}))

	exporter := modbus.NewExporter(config)
	http.Handle("/modbus",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scrapeHandler(exporter, w, r, logger)
		}),
	)

	srv := &http.Server{}
	if err := web.ListenAndServe(srv, toolkitFlags, logger); err != nil {
		level.Error(logger).Log("msg", "Error starting HTTP server", "err", err)
		os.Exit(1)
	}
}

func scrapeHandler(e *modbus.Exporter, w http.ResponseWriter, r *http.Request, logger log.Logger) {
	moduleName := r.URL.Query().Get("module")
	if moduleName == "" {
		http.Error(w, "'module' parameter must be specified", http.StatusBadRequest)
		return
	}

	if !e.GetConfig().HasModule(moduleName) {
		http.Error(w, fmt.Sprintf("module '%v' not defined in configuration file", moduleName), http.StatusBadRequest)
		return
	}

	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "'target' parameter must be specified", http.StatusBadRequest)
		return
	}

	sT := r.URL.Query().Get("sub_target")
	if sT == "" {
		http.Error(w, "'sub_target' parameter must be specified", http.StatusBadRequest)
		return
	}

	subTarget, err := strconv.ParseUint(sT, 10, 32)
	if err != nil {
		http.Error(w, fmt.Sprintf("'sub_target' parameter must be a valid integer: %v", err), http.StatusBadRequest)
		return
	}
	if subTarget > 255 {
		http.Error(w, fmt.Sprintf("'sub_target' parameter must be from 0 to 255. Invalid value: %d", subTarget), http.StatusBadRequest)
		return
	}

	level.Info(logger).Log("msg", "got scrape request", "module", moduleName, "target", target, "sub_target", subTarget)

	gatherer, err := e.Scrape(target, byte(subTarget), moduleName)
	if err != nil {
		httpStatus := http.StatusInternalServerError
		if strings.Contains(fmt.Sprintf("%v", err), "unable to connect with target") {
			httpStatus = http.StatusServiceUnavailable
		} else if strings.Contains(fmt.Sprintf("%v", err), "i/o timeout") {
			httpStatus = http.StatusGatewayTimeout
		}
		http.Error(
			w,
			fmt.Sprintf("failed to scrape target '%v' with module '%v': %v", target, moduleName, err),
			httpStatus,
		)
		level.Error(logger).Log("msg", "failed to scrape", "target", target, "module", moduleName, "err", err)
		return
	}

	promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}
