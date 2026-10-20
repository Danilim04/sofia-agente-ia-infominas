# Sofia — agente de atendimento WhatsApp da Infominas

**Sofia** é uma atendente virtual que recebe a reclamação do cliente final da
Infominas pelo WhatsApp (via **Infodesk**, o sistema de atendimento e tickets da
empresa), conversa de forma humana, **classifica** o problema com o **JEV**
(TypeSafe — classificação determinística com confiança), **roteia** a conversa
para o departamento certo, **abre o ticket** na mesa certa e **transfere para um
atendente humano** quando o pedido, a mídia, a confiança ou o tom exigem. O
código nasce da refatoração do Zapin (bot de suporte da Azapfy): gateway **Go**
(`backend/`: webhook, fila por conversa, cliente Infodesk, idempotência de
tickets) + cérebro **Python** (`agente-ia/`: LangGraph, classificação, guardrails,
tools com política, RAG), ligados pelo Contrato A (`POST /chat`).

Para rodar: crie o venv do cérebro (`cd agente-ia && uv venv venv --python 3.12
&& uv pip install -r requirements.txt -r requirements-dev.txt`), compile o
gateway (`cd backend && go build ./...`) e valide tudo com
`scripts/verificar.sh --rapido` (lint, tipos, arquitetura e testes — o mesmo que
o hook de commit e o CI rodam). A stack local completa (cérebro + gateway +
`mock-infodesk`) sobe com `scripts/start-all.sh` a partir da Onda 3; detalhes,
variáveis e dicas para Windows na skill `.claude/skills/ambiente-testes/SKILL.md`.

O plano está em [`docs/plano/`](docs/plano/00-visao-geral.md) (arquitetura,
[catálogo de tools](docs/plano/01-ferramentas.md), [classificação](docs/plano/02-classificacao.md),
[etapas em ondas](docs/plano/03-etapas.md), [esteira de qualidade](docs/plano/04-esteira-qualidade.md),
[agentes](docs/plano/05-agentes.md), [harness](docs/plano/06-harness.md)); a API do
Infodesk em [`docs/infodesk/`](docs/infodesk/README.md); as decisões em
[`docs/decisoes/`](docs/decisoes/README.md); o legado do Zapin, só como
referência, em `docs/zapin/`. Quem trabalha com o Claude Code começa pelo
[`CLAUDE.md`](CLAUDE.md).
