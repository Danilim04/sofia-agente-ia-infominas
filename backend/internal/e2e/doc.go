// Package e2e sobe a stack da Sofia inteira (mock-infodesk, gateway Go e um
// cérebro falso que responde o Contrato A de forma scriptada) e roda os
// cenários de ponta a ponta da etapa E4.1.
//
// Os testes só existem com a build tag `e2e` (não rodam no `go test ./...`):
//
//	cd backend && go test -tags e2e ./internal/e2e/... -v
//
// Pré-requisitos: Go (compila o gateway) e Python 3.12+ (o mock-infodesk é
// stdlib, sem pip). `E2E_PYTHON` aponta o interpretador; sem ele o teste
// procura o venv do agente-ia e depois python3/python no PATH. Nenhuma rede
// externa e nenhuma chave: o cérebro falso substitui LLM e JEV.
//
// Com `E2E_CEREBRO=real` (e OPENROUTER_API_KEY) o teste sobe o cérebro Python
// de verdade no lugar do falso; os cenários que dependem de uma resposta
// scriptada (ticket pela conversa) são pulados nesse modo.
package e2e
