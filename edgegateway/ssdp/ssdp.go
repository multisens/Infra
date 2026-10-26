package main

import (
	"bufio"
	"fmt"
	"strings"
	"time"
)

// SSDP messages (HTTPU): two announced types (the URN of the service and the
// UDN of the root device), USN "<UDN>::<URN>" and "<UDN>".

const (
	ssdpGroup  = "239.255.255.250"
	ssdpPort   = 1900
	maxAge     = 1800
	serverSig  = "Linux UPnP/1.1 tv30-ssdp/1.0"
	ssdpAll    = "ssdp:all"
	ssdpAlive  = "ssdp:alive"
	ssdpByebye = "ssdp:byebye"
)

type usn struct{ NT, USN string }

func usns(udn string) []usn {
	return []usn{{ssdpST, udn + "::" + ssdpST}, {udn, udn}}
}

func notify(u usn, location string, alive bool) string {
	var b strings.Builder
	b.WriteString("NOTIFY * HTTP/1.1\r\n")
	fmt.Fprintf(&b, "HOST: %s:%d\r\n", ssdpGroup, ssdpPort)
	fmt.Fprintf(&b, "NT: %s\r\n", u.NT)
	if alive {
		fmt.Fprintf(&b, "NTS: %s\r\n", ssdpAlive)
	} else {
		fmt.Fprintf(&b, "NTS: %s\r\n", ssdpByebye)
	}
	fmt.Fprintf(&b, "USN: %s\r\n", u.USN)
	if alive {
		fmt.Fprintf(&b, "LOCATION: %s\r\n", location)
		fmt.Fprintf(&b, "CACHE-CONTROL: max-age=%d\r\n", maxAge)
		fmt.Fprintf(&b, "SERVER: %s\r\n", serverSig)
	}
	b.WriteString("\r\n")
	return b.String()
}

// response: the unicast reply to an M-SEARCH, with the max-age of the NOTIFY.
func response(st string, u usn, location string, now time.Time) string {
	var b strings.Builder
	b.WriteString("HTTP/1.1 200 OK\r\n")
	fmt.Fprintf(&b, "ST: %s\r\n", st)
	fmt.Fprintf(&b, "USN: %s\r\n", u.USN)
	fmt.Fprintf(&b, "CACHE-CONTROL: max-age=%d\r\n", maxAge)
	fmt.Fprintf(&b, "DATE: %s\r\n", now.UTC().Format(time.RFC1123))
	fmt.Fprintf(&b, "SERVER: %s\r\n", serverSig)
	b.WriteString("EXT: \r\n")
	fmt.Fprintf(&b, "LOCATION: %s\r\n", location)
	b.WriteString("\r\n")
	return b.String()
}

// parseMSearch returns the ST of a valid M-SEARCH (MAN "ssdp:discover", MX
// and ST present), or "" if the message is not one.
func parseMSearch(msg string) string {
	sc := bufio.NewScanner(strings.NewReader(msg))
	if !sc.Scan() || !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sc.Text())), "M-SEARCH * HTTP/1.1") {
		return ""
	}
	h := map[string]string{}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			break
		}
		if i := strings.Index(line, ":"); i > 0 {
			h[strings.ToUpper(strings.TrimSpace(line[:i]))] = strings.TrimSpace(line[i+1:])
		}
	}
	if !strings.Contains(strings.ToLower(h["MAN"]), "ssdp:discover") || h["MX"] == "" || h["ST"] == "" {
		return ""
	}
	return strings.Trim(h["ST"], `"`)
}

// answers returns the replies to an ST: ssdp:all answers for every type,
// with the ST of each; another ST is answered only if it is announced.
func answers(st, udn, location string, now time.Time) []string {
	var out []string
	for _, u := range usns(udn) {
		switch {
		case st == ssdpAll:
			out = append(out, response(u.NT, u, location, now))
		case st == u.NT:
			out = append(out, response(st, u, location, now))
		}
	}
	return out
}
