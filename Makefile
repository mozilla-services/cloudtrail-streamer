all: build

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

lambda: clean build
	apt-get update
	apt-get install -y zip
	zip cloudtrail-streamer.zip cloudtrail-streamer

build:
	vgo build -ldflags="-s -w" -o cloudtrail-streamer

debug: build
	env CT_DEBUG_LOGGING=1 ./cloudtrail-streamer

clean:
	rm -f cloudtrail-streamer cloudtrail-streamer.zip

test:
	go test ./...
