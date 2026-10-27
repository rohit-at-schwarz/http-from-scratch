package myHttp

type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return e.Message
}

func newHTTPError(code int, msg string) error {
	return &HTTPError{StatusCode: code, Message: msg}
}
