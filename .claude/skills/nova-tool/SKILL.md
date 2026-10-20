---
name: nova-tool
description: Como criar ou alterar uma ferramenta (tool) do agente seguindo o padrão do Zapin — tool Python com InjectedToolArg + entrada em POLITICAS, endpoint correspondente em backend/internal/toolsapi, contrato JSON em contratos/ e testes dos dois lados. Carregue ao implementar qualquer item de docs/plano/01-ferramentas.md ou ao tocar tool_policy.py / toolsapi.go.
---

# Nova tool: o caminho obrigatório

Catálogo: `docs/plano/01-ferramentas.md` (cada tool tem lá args, injetados,
validações, endpoint, erros). Princípio: **o LLM propõe, o código dispõe** —
autorização, escopo, idempotência e formatação nunca são delegados ao modelo.

## Checklist (nesta ordem)

1. **Contrato primeiro**: `contratos/tools-infodesk/<endpoint>/{request.json, ok.json, erro-<motivo>.json}`
   com os campos do `01`. Erro de negócio é HTTP 200 `{"status": false, "erro": "<pt-BR para o LLM>", "motivo": "<codigo>"}`.
2. **Go** (`backend/internal/toolsapi/`): handler `POST /tools/infodesk/<area>/<acao>`
   embrulhado em `h.auth` (`X-Tools-Token`, `subtle.ConstantTimeCompare`), `decode` com `MaxBytesReader` 64 KiB,
   identidade/escopo resolvidos **do store pelo telefone**, nunca do corpo; chama `infodesk.Client` via interface
   (`InfodeskAPI`) para o teste usar `fakeInfodesk`. Teste de contrato: carrega os JSON de `contratos/` e compara.
3. **Python** (`agente-ia/src/tools/infodesk_tools.py`): função `@tool`; args de sessão como
   `Annotated[str, InjectedToolArg] = ""`; docstring = instrução ao LLM (quando chamar, o que fazer com cada `motivo`,
   `Args:`/`Returns:`), curta e sem segredo; corpo só chama `_post(path, payload)`.
4. **Política** (`agente-ia/src/agent/tool_policy.py`): entrada em `POLITICAS` com `injetar` (todo arg de sessão),
   `validar` (todo arg do LLM que referencie escopo, número, enum) e `exigir` (precondição de sessão). Sem entrada → o
   teste de arquitetura falha.
5. **Efeito no estado** (`agente-ia/src/tools/efeitos.py`): se a tool carimba/consome algo do estado (proposta,
   identificação), registre em `EFEITOS[nome]`; `tools_node` chama `efeito_pos_tool`.
6. **Lista canônica**: adicione em `get_default_tools()` (`src/agent/graph.py`).
7. **Testes**: Python — política (cada recusa), tool com `httpx` monkeypatchado devolvendo cada JSON de `contratos/`,
   e2e com LLM scriptado (ver `tests/fakes.py`); Go — handler com `httptest` + `fakeInfodesk` + SQLite em `t.TempDir()`.
8. **Docs**: linha nova/alterada em `docs/plano/01-ferramentas.md`; se mudou decisão, ADR.

## Modelo Python

```python
@tool
def consultar_status_chamado(
    numero: str,
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
    identificacao: Annotated[Optional[dict], InjectedToolArg] = None,
) -> dict[str, Any]:
    """Consulta o andamento de um chamado pelo número que o cliente informou.

    Use quando o cliente perguntar sobre um chamado existente e já tiver dito o
    número. Se vier motivo "posse_nao_verificada", diga que não consegue confirmar
    esse chamado por aqui e ofereça um atendente — não invente status.

    Args:
        numero: número do chamado, só dígitos.

    Returns:
        dict: status (bool); em sucesso numero, status_ticket, titulo, aberto_em.
    """
    return _post("/tools/infodesk/ticket/consultar", {"telefone": telefone, "conversation_id": conversation_id,
                                                      "numero": numero, "identificacao": identificacao})
```

```python
"consultar_status_chamado": PoliticaTool(
    injetar={"telefone": _telefone_da_sessao, "conversation_id": _conversation_id_da_sessao,
             "identificacao": _identificacao_da_sessao},
    validar={"numero": _validar_numero_chamado},
),
```

## Modelo Go (handler)

```go
func (h *Handler) handleConsultar(w http.ResponseWriter, r *http.Request) {
    var req reqConsultar
    if !decode(w, r, &req) { return }
    if !numeroValido(req.Numero) { writeJSON(w, 200, recusa("número inválido", "campo_invalido")); return }
    t, ok, err := h.store.TicketPorNumero(r.Context(), req.Numero)
    // posse: só devolve se t.Telefone == req.Telefone (ou schema do Infodesk permitir — TODO(P3))
    ...
    writeJSON(w, http.StatusOK, map[string]any{"status": true, "numero": t.Numero, ...})
}
```

## Efeito colateral? Duas fases, sempre

`preparar_*` (dry-run, devolve `proposta`, `efeitos.py` carimba no estado) →
confirmação explícita do cliente no turno seguinte (checada por código:
`CONFIRMACOES_EXPLICITAS`) → `executar_*` **sem argumentos do LLM** (a
proposta é injetada; `exigir=_exigir_proposta_confirmada`). Idempotência por
chave calculada da proposta (ADR 0005).

## O que nunca fazer

- Expor `telefone`, `conversation_id`, `classificacao`, `proposta`,
  `identificacao`, `mesa`, `departamento`, `prioridade` como argumento do LLM.
- Chamar o Infodesk do Python.
- Repetir um POST com efeito em retry.
- Devolver ao LLM o texto cru de um erro HTTP.
