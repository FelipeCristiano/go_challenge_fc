#!/bin/sh
# LocalStack init script — provisiona as filas SQS FIFO
set -e

echo "==> Criando filas SQS FIFO..."

awslocal sqs create-queue \
  --queue-name wager-transactions-dlq.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false \
  --region us-east-1

awslocal sqs create-queue \
  --queue-name wager-transactions.fifo \
  --attributes '{
    "FifoQueue": "true",
    "ContentBasedDeduplication": "false",
    "VisibilityTimeout": "60",
    "MessageRetentionPeriod": "86400",
    "RedrivePolicy": "{\"deadLetterTargetArn\":\"arn:aws:sqs:us-east-1:000000000000:wager-transactions-dlq.fifo\",\"maxReceiveCount\":\"5\"}"
  }' \
  --region us-east-1

# Fila de eventos de saída (outbox publica aqui)
awslocal sqs create-queue \
  --queue-name wager-events.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false \
  --region us-east-1

echo "==> Filas SQS criadas com sucesso."
awslocal sqs list-queues --region us-east-1
