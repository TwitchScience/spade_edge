#!/bin/bash --
set -euo pipefail

PROJECT=$1
BRANCH=$2
SOURCE_AMI=$3
VPC=$4
SUBNET=$5
SECURITY_GROUP=$6

USE_PRIVATE_IP=${7:-"false"}

export GOARCH=amd64
export GOOS=linux

SEEKER_DEBIAN="http://science.twitch.tv/debs/seeker_1.0.3_amd64.deb"

bash run_tests.sh
go install -v ./...
gometalinter ./... --disable gocyclo --disable dupl --disable gas --deadline 30s

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
     build/packer.json | tee build.log

AMIREF=`grep 'amazon-ebs,artifact,0,id,' build.log`
echo ${AMIREF##*:} > amireference
