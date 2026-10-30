# CAP: HTTP from Scratch

This is the base for the training "HTTP from Scratch".
Purpose of this training is to deepen Go development and understanding of the HTTP protocol.

## Intro

The HyperText Transfer Protocol (HTTP) is the basis for the „Internet“. 
In this training, we'll implement the still widely supported and used version 1.1, e.g.

```http
GET /index.html HTTP/1.1
Host: www.example.com
User-Agent: Browser
```

returns

```
HTTP/1.1 200 OK
Server: nginx/1.15.8
Date: Fri, 09 Apr 2021 13:34:03 GMT
Content-Type: text/html
Content-Length: 235
<html>
  <head>
  <title>Hello World</title>
</head>
<body>
  Hello World
</body>
</html>
```

## The Task (Day 1)

* Read [the RFC](https://www.rfc-editor.org/info/rfc9112/)
* Implement a basic HTTP server that
  * can handle client connections
  * parse requests
  * write responses

Until this point, it is fine to use the http.DefaultServeMux.

# Observability Task


## 1. Setup KIND Cluster locally


KIND (Kubernetes in Docker) is a quick and easy solution to run a cluster locally for testing purposes.


See [Quick Start](https://kind.sigs.k8s.io/docs/user/quick-start/)


## 2. Deploy your server to KIND


In order to test your metrics integration, its best to have your server running in k8s as well. At the same time, its highly recommend to automate the process of building and deploying it, in order to reduce roundtrip times.


In order to do so, you need to


Create a Dockerfile for your server
Create a k8s deployment, service and ingress yaml for your server
Create a Makefile with the following targets:
  * make image (should build the docker image)
  * make deploy-local (depends on image, uploads it to kind and applies k8s yamls)


At the end of the task, you should be able to reach your server in the cluster after a single make deploy-local


## 3. Setup vmstack in KIND


You can use helm to install the [victoria metrics stack](https://artifacthub.io/packages/helm/victoriametrics/victoria-metrics-k8s-stack) to your kind cluster.


## 4. Scrape metrics from the server


After having your server and the VM Stack next to each other in the cluster, you can expose an endpoint on your server that makes the metrics accessbile and scrape it using [VMServiceScrape](https://docs.victoriametrics.com/operator/resources/vmservicescrape/).