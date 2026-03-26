// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// The sandbox-router is a reverse proxy for routing requests
// to sandbox agents.
// It reads routing information from HTTP headers and forwards requests
// to the corresponding sandbox service within a Kubernetes cluster.
package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	director := func(req *http.Request) {
		sandboxID := req.Header.Get("X-Sandbox-Id")
		sandboxNamespace := req.Header.Get("X-Sandbox-Namespace")
		sandboxPort := req.Header.Get("X-Sandbox-Port")

		if sandboxID == "" {
			sandboxID = req.Header.Get("x-sandbox-id")
		}
		if sandboxNamespace == "" {
			sandboxNamespace = req.Header.Get("x-sandbox-namespace")
		}
		if sandboxPort == "" {
			sandboxPort = req.Header.Get("x-sandbox-port")
		}

		if sandboxID == "" || sandboxNamespace == "" || sandboxPort == "" {
			log.Printf("Missing required headers: ID=%v, Namespace=%v, Port=%v", sandboxID, sandboxNamespace, sandboxPort)
			return
		}

		targetURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:%s", sandboxID, sandboxNamespace, sandboxPort)
		target, err := url.Parse(targetURL)
		if err != nil {
			log.Printf("Failed to parse target URL %s: %v", targetURL, err)
			return
		}

		log.Printf("Proxying request %s to %s", req.URL.Path, targetURL)

		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
	}

	proxy := &httputil.ReverseProxy{
		Director: director,
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLS: func(network, addr string, cfg *tls.Config) (net.Conn, error) {
				return net.Dial(network, addr)
			},
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("Proxy error for %s: %v", r.URL.Path, err)
			http.Error(w, "Proxy error: "+err.Error(), http.StatusBadGateway)
		},
		ModifyResponse: func(res *http.Response) error {
			if res.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(res.Body)
				log.Printf("Backend returned error %d: %s", res.StatusCode, string(body))
			}
			return nil
		},
	}

	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok\n"))
			return
		}
		proxy.ServeHTTP(w, r)
	}

	server := &http.Server{
		Addr:    ":" + port,
		Handler: h2c.NewHandler(http.HandlerFunc(handler), &http2.Server{}),
	}

	log.Printf("Starting sandbox router on port %s...", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
