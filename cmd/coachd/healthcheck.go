package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// healthcheckMain probes the ops listener's /livez; it needs no config beyond
// COACHD_OPS_ADDR so the container HEALTHCHECK works without a timezone.
func healthcheckMain() int { return runHealthcheck(healthAddr(os.Getenv("COACHD_OPS_ADDR"))) }

// healthAddr turns a listen address into a dialable one (":9090" -> loopback).
func healthAddr(listen string) string {
	if listen == "" {
		listen = ":9090"
	}
	if strings.HasPrefix(listen, ":") {
		return "127.0.0.1" + listen
	}
	return listen
}

func runHealthcheck(addr string) int {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + addr + "/livez")
	if err != nil {
		fmt.Fprintln(os.Stderr, "coachd healthcheck: unreachable")
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "coachd healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
