#!/usr/bin/env bash
# Inicializa o ambiente
# ./scripts/dev-up.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"

echo "==> Subindo infraestrutura (postgres, keycloak, localstack)..."
docker compose -f "$ROOT_DIR/docker-compose.yml" up -d postgres keycloak localstack

echo "==> Aguardando PostgreSQL ficar saudável..."
until docker compose -f "$ROOT_DIR/docker-compose.yml" exec postgres \
  pg_isready -U desafio -d desafio -q; do
  sleep 2
done

echo "==> Criando banco keycloak (se não existir)..."
docker compose -f "$ROOT_DIR/docker-compose.yml" exec postgres \
  psql -U desafio -tc "SELECT 1 FROM pg_database WHERE datname = 'keycloak'" | \
  grep -q 1 || \
  docker compose -f "$ROOT_DIR/docker-compose.yml" exec postgres \
  psql -U desafio -c "CREATE DATABASE keycloak"

echo "==> Aplicando migrations..."
docker compose -f "$ROOT_DIR/docker-compose.yml" run --rm migrate

echo "==> Ambiente pronto!"
echo ""
echo "    PostgreSQL : localhost:5432  (desafio/desafio)"
echo "    Keycloak   : http://localhost:8080  (admin/admin)"
echo "    LocalStack : http://localhost:4566"
echo ""
echo "Para rodar a aplicação: go run ./cmd/server"
