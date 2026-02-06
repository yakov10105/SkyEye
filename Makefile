.PHONY: build test protoc

build:
	@echo "Building services..."
	# Go build commands will go here

test:
	@echo "Running tests..."
	# Go test commands will go here

protoc:
	@echo "Generating protobuf code..."
	protoc --proto_path=api/v1 --go_out=./api/v1 --go_opt=paths=source_relative --go-grpc_out=./api/v1 --go-grpc_opt=paths=source_relative api/v1/scraper.proto
