all: cloudtrail_streamer

# Package lambda function in zip file
package:
	docker run -i --rm -v `pwd`:/go/src/github.com/mozilla-services/cloudtrail-streamer \
		golang:1.10 \
		/bin/bash -c 'cd /go/src/github.com/mozilla-services/cloudtrail-streamer && make lambda'

# Development target, upload package to s3
packageupload:
	@if [[ -z "$(CT_DEV_S3_BUCKET)" ]]; then \
		echo "set CT_DEV_S3_BUCKET in environment"; \
		exit 1; \
	fi
	aws s3 cp cloudtrail-streamer.zip s3://$(CT_DEV_S3_BUCKET)/cloudtrail-streamer.zip

lambda: clean dep cloudtrail_streamer
	apt-get update
	apt-get install -y zip
	zip cloudtrail-streamer.zip cloudtrail-streamer

dep:
	go get ./...

cloudtrail_streamer:
	go build -ldflags="-s -w" -o cloudtrail-streamer

debug: cloudtrail_streamer
	env CT_DEBUG=1 ./cloudtrail-streamer

clean:
	rm -f cloudtrail-streamer cloudtrail-streamer.zip

test: dep
	go test ./...
