#!/bin/bash --
set -euo pipefail

# ossareh(20150109): Perhaps use something like:
# http://stackoverflow.com/questions/192249/how-do-i-parse-command-line-arguments-in-bash
PROJECT=$1
BRANCH=$2
SOURCE_AMI=$3
VPC=$4
SUBNET=$5
SECURITY_GROUP=$6

# I hate boolean args, but I'm not sure how to handle this.
USE_PRIVATE_IP=${7:-"false"}

export GOARCH=amd64
export GOOS=linux

SEEKER_DEBIAN="http://science.twitch.tv/debs/seeker_1.0.3_amd64.deb"

godep restore
godep go vet ./...
godep go test -v ./...
godep go install -v ./...

packer                                          \
     build -machine-readable                    \
     -var "project=${PROJECT}"                  \
     -var "branch=${BRANCH}"                    \
     -var "source_ami=${SOURCE_AMI}"            \
     -var "vpc_id=${VPC}"                       \
     -var "subnet_id=${SUBNET}"                 \
     -var "security_group_id=${SECURITY_GROUP}" \
     -var "use_private_ip=${USE_PRIVATE_IP}"    \
     -var "binary_dir"=${GOPATH}/bin            \
     -var "scripts_dir"=build/scripts           \
     -var "seeker_debian=${SEEKER_DEBIAN}"      \
     build/packer.json
