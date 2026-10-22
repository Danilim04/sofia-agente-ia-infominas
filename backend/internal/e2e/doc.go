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
// Limites conhecidos:
//
//   - o cenário 1 usa um solicitante cadastrado do mock e roda UMA vez por
//     processo (com -count=N as repetições são puladas, com aviso);
//   - E2E_ESTRITO=1 transforma em falha o bug conhecido registrado no
//     cenário 1 (BUG-E4.1-1);
//   - E2E_CEREBRO=real (+ OPENROUTER_API_KEY) sobe o cérebro Python no lugar do
//     falso, mas só os cenários 2 e 3 (que não dependem do roteiro) valem nesse
//     modo; os demais são pulados. O modo real ainda não foi exercitado.
package e2e
