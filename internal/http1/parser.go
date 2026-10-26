package http1

import (
	"bufio"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func ParseRequest(reader *bufio.Reader) (*http.Request, error) {
	ln, err := reader.ReadString('\n')
	if err != nil {
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

	return request, nil
}
