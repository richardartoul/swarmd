#!/bin/sh
set -eu

APP_NAME=demo-api
IMAGE_TAG="${IMAGE_TAG:-latest}"

echo "Deploying ${APP_NAME}:${IMAGE_TAG}"
kubectl apply -f ./k8s/deployment.yaml
kubectl rollout status deployment/${APP_NAME} --timeout=120s
