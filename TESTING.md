# Como Testar — evaluation-service

## Pré-requisitos

- Go 1.21+
- `go-junit-report` e `gocyclo` instalados (ver passo 1)

> **PATH necessário (Windows — instalação portável):**
> Adicione ao PATH do seu usuário antes de usar:
> ```powershell
> $env:PATH = "$env:USERPROFILE\go-portable\go\bin;$env:USERPROFILE\go\bin;$env:PATH"
> ```
> Para tornar permanente, execute uma vez:
> ```powershell
> $p = "$env:USERPROFILE\go-portable\go\bin;$env:USERPROFILE\go\bin"
> [Environment]::SetEnvironmentVariable("PATH", "$p;" + [Environment]::GetEnvironmentVariable("PATH","User"), "User")
> ```

---

## 1. Instalar ferramentas auxiliares (fazer uma vez)

```powershell
# Gerador de relatório JUnit XML a partir da saída do go test
go install github.com/jstemmer/go-junit-report/v2@latest

# Calculador de complexidade ciclomática
go install github.com/fzipp/gocyclo/cmd/gocyclo@latest
```

---

## 2. Baixar dependências do projeto

```powershell
go mod download
```

---

## 3. Executar os testes

### Só os testes (resultado rápido)
```powershell
go test ./... -v
```

### Testes com cobertura (gera coverage.out)
```powershell
go test ./... -v "-coverprofile=coverage.out" "-covermode=atomic"
```

> ⚠️ No PowerShell os argumentos com `=` precisam de aspas. No bash/Linux as aspas são opcionais.

### Ver cobertura por função no terminal
```powershell
go tool cover "-func=coverage.out"
```

### Ver cobertura em HTML no navegador
```powershell
go tool cover "-html=coverage.out" -o coverage.html
# Abrir o arquivo coverage.html no navegador
```

### Tudo junto + gerar JUnit XML (equivalente ao que roda na pipeline)
```powershell
go test ./... -v "-coverprofile=coverage.out" "-covermode=atomic" 2>&1 | Tee-Object -FilePath test-output.txt
go tool cover "-func=coverage.out" | Tee-Object -FilePath coverage-func.txt
cat test-output.txt | go-junit-report -set-exit-code > test-results.xml
```

---

## 4. Executar lint

### Instalar golangci-lint (fazer uma vez)
```powershell
go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.57.2
```

### Verificar problemas no código
```powershell
golangci-lint run ./...
```

### Ver apenas por linha (mais legível)
```powershell
golangci-lint run ./... --out-format line-number
```

### Exportar em JSON (formato usado pela pipeline CI)
```powershell
golangci-lint run ./... --out-format json > golangci-output.json
```

#### Principais linters incluídos no golangci-lint

| Linter | O que verifica |
|--------|----------------|
| `errcheck` | Erros retornados e ignorados |
| `gosimple` | Simplificações de código |
| `govet` | Erros de construção suspeitos |
| `ineffassign` | Atribuições que nunca são usadas |
| `staticcheck` | Bugs estáticos e código morto |
| `unused` | Funções e variáveis não utilizadas |

> 💡 O lint **não quebra a pipeline** — apenas reporta no Job Summary.

---

## 5. Calcular complexidade ciclomática

### Todas as funções ordenadas por complexidade
```powershell
gocyclo -over 1 . | Sort-Object { [int]($_ -split ' ')[0] } -Descending
```

### Apenas funções com risco moderado ou alto (score >= 5)
```powershell
gocyclo -over 5 .
```

#### Legenda de scores (gocyclo)

| Score | Risco            |
|-------|------------------|
| 1–4   | 🟢 Baixo         |
| 5–9   | 🟡 Moderado      |
| 10+   | 🔴 Alto          |

---

## 6. Resultados atuais

| Métrica | Valor |
|---------|-------|
| Testes  | ✅ 15/15 passando |
| Cobertura total | 60.1% |
| Funções 100% cobertas | `runEvaluationLogic`, `getDeterministicBucket`, `evaluationHandler`, `fetchFromServices`, `getDecision` |
| Função mais complexa | `main` — score 13, `runEvaluationLogic` — score 8 |

> A cobertura de `main()` é 0% por design — ela inicializa Redis, SQS e HTTP server,
> dependências que requerem infraestrutura real.
> `sendEvaluationEvent` tem 25% pois os caminhos que enviam ao SQS real não são
> executados nos testes unitários.

---

## 7. Arquivos gerados (não comitar)

Adicione ao `.gitignore`:

```
coverage.out
coverage.html
coverage-func.txt
test-output.txt
test-results.xml
cyclo-output.txt
*.exe
```

---

## Estratégia dos testes

Os testes são **unitários** — todas as dependências externas são mockadas:

| Dependência real         | Como é simulada nos testes                                              |
|--------------------------|-------------------------------------------------------------------------|
| Redis                    | `redis.NewClient` com `DialTimeout: 1ms` → sempre cache miss imediato  |
| flag-service (HTTP)      | `httptest.NewServer` substituindo a URL real                            |
| targeting-service (HTTP) | `httptest.NewServer` substituindo a URL real                            |
| AWS SQS                  | `SqsQueueURL: ""` → `sendEvaluationEvent` registra log e retorna        |

Nenhuma conexão real com Redis, AWS ou outros serviços é feita. Os testes podem
rodar sem Docker, sem credenciais AWS e sem infraestrutura.
