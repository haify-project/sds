#!/bin/bash
# Generate protobuf code

protoc --proto_path=. --proto_path=third_party \
    --go_out=. --go_opt=paths=source_relative \
    --go-grpc_out=. --go-grpc_opt=paths=source_relative \
    --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
    --openapiv2_out=. --openapiv2_opt=allow_merge=true,merge_file_name=sds \
    api/proto/v1/sds.proto

echo "Proto files generated"
