#!/bin/bash
# Generate protobuf code

protoc --proto_path=. \
    --go_out=. --go_opt=paths=source_relative \
    --go-grpc_out=. --go-grpc_opt=paths=source_relative \
    api/proto/v1/sds.proto

echo "Proto files generated"
