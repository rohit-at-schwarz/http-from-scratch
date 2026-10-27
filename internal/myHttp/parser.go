package myHttp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	intentUnspecified = iota // 0: client didn't send a Connection header
	intentToKeepAlive        // 1: client explicitly sent "keep-alive"
	intentToClose            // 2: client explicitly sent "close"
)

func shouldKeepConnAlive(protocol string, header http.Header) bool {
	intent := intentUnspecified

	// GET /hello HTTP/1.1
	// Host: example.com
	// Connection: keep-alive, close
	for _, v := range header.Values("Connection") {
		for part := range strings.SplitSeq(v, ",") {
			part = strings.TrimSpace(strings.ToLower(part))
			switch part {
			case "close":
				intent = intentToClose
			case "keep-alive":
				if intent != intentToClose {
					intent = intentToKeepAlive
				}
			}
		}
	}

	switch protocol {
	case "HTTP/1.1":
		return intent != intentToClose //stays open by default, unless the intent was to close
	case "HTTP/1.0":
		return intent == intentToKeepAlive //closes by default, unless the intent was to keep-alive
	default:
		return false
	}
}

func ParseRequest(reader *bufio.Reader) (*http.Request, error) {
	ln, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(ln) == 0 {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("failed to read the request line: %w", err)
	}

	//this will give us something like this 'GET /hello HTTP/1.1' from 'GET /hello HTTP/1.1\r\n'
	ln = strings.TrimRight(ln, "\r\n")
	subStrings := strings.Split(ln, " ")
	if len(subStrings) != 3 {
		return nil, fmt.Errorf("malformed request line: %q", ln)
	}

	method, rawURI, protocol := subStrings[0], subStrings[1], subStrings[2]
	parsedURL, err := url.ParseRequestURI(rawURI)
	if err != nil {
		return nil, fmt.Errorf("invalid request URI: %w", err)
	}

	request := &http.Request{
		Method:     method,
		URL:        parsedURL,
		Proto:      protocol,
		Header:     make(http.Header),
		RequestURI: rawURI,
	}

	//now that we have the first line parsed already, we can parse the read of the lines
	//as well in a for loop, since all of them have the same structure of 1 entry per line
	//and a format of <key>: <val>, we can use deterministic parsing here to get all the headers
	for {
		currHeaderLn, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("failed to read header line: %w", err)
		}

		currHeaderLn = strings.TrimRight(currHeaderLn, "\r\n")
		if currHeaderLn == "" {
			//empty line means the header section is done (\r\n\r\n reached)
			break
		}

		idxCol := strings.IndexByte(currHeaderLn, ':')
		if idxCol == -1 {
			return nil, fmt.Errorf("malformed header: %q", currHeaderLn)
		}

		currKey := strings.TrimSpace(currHeaderLn[0:idxCol])
		currVal := strings.TrimSpace(currHeaderLn[idxCol+1:])

		request.Header.Add(currKey, currVal)
	}

	//apparently the router and any standard middleware expect req.Host to be
	//populated so they can route requests based on the domain
	request.Host = request.Header.Get("Host")

	clVal := request.Header.Get("Content-Length")
	if clVal != "" {
		cl, err := strconv.ParseInt(clVal, 10, 64)
		if err != nil || cl < 0 {
			return nil, fmt.Errorf("invalid Content-Length: %q", clVal)
		}
		request.ContentLength = cl
		request.Body = io.NopCloser(io.LimitReader(reader, cl))
	} else {
		request.Body = http.NoBody
	}

	return request, nil
}
