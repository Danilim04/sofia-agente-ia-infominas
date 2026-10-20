# 0001 — Manter gateway Go + cérebro Python

- **Estado:** aceita (2026-10-19)
- **Etapa:** planejamento
- **Pendências relacionadas:** P10

## Contexto

O Zapin separa transporte (Go) de raciocínio (Python). Para a Infominas o
transporte muda (Infodesk em vez de Chatwoot) e a identidade some (sem login).
Um único processo Python seria mais simples de operar.

## Decisão

Manter os dois processos e o Contrato A (`POST /chat`). O Go continua dono de:
webhook + HMAC, dedup, fila FIFO + coalescência por conversa, estado
bot/humano, cliente HTTP do Infodesk, idempotência de tickets, transferência,
bolhas WhatsApp, alertas. O Python continua dono de: grafo, classificação
(JEV), guardrails, RAG, tools (via `/tools/infodesk/*` do gateway), persona.
Módulo Go renomeado para `bot-infominas`.

## Consequências

- Reaproveita o chassi testado (coalescer, disjuntor, transferência única,
  store em dois backends) e o padrão `toolsapi` + `tool_policy`.
- Dois processos para operar e dois conjuntos de testes/lint (a esteira cobre
  os dois com um script).
- O webhook do Infodesk exige `200` em 8 s: a fila assíncrona do Go já resolve.

## Alternativas descartadas

- Só Python (FastAPI recebe o webhook): perde a serialização por conversa e a
  resiliência já prontas; reescrever isso em Python custa mais que adaptar.
- Só Go com LLM direto: perde LangGraph, guardrails e o ecossistema de tools.
