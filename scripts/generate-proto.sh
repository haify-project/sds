#!/bin/bash
# Generate protobuf code

protoc --go_out=. --go_opt=paths=source_relative \
    --go-grpc_out=. --go-grpc_opt=paths=source_relative \
    proto/api/v1/controller.proto

echo "Proto files generated"
