#!/bin/bash
docker build -t us-central1-docker.pkg.dev/scenic-dynamo-439619-t4/crypto-telegram-notificator/telegram-handler .
docker push us-central1-docker.pkg.dev/scenic-dynamo-439619-t4/crypto-telegram-notificator/telegram-handler
