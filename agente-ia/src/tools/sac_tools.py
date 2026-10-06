"""Tools reais de chamados (SAC) — preparar (dry-run), abrir, listar, tipos.

A abertura é em DUAS FASES (determinístico, sem depender do modelo):
`preparar_abertura_chamado` valida tudo no gateway sem criar nada e a proposta
aprovada fica no estado do grafo; após a confirmação do cliente,
`abrir_chamado_suporte` executa EXATAMENTE essa proposta (injetada via
`tool_policy` — o modelo não passa argumento nenhum na abertura).

Estas tools **não** falam direto com o SAC: elas chamam o gateway Go
(`/tools/sac/*`), que resolve a identidade do relator a partir do telefone
(cache do gate) e monta a requisição. Assim os campos do RELATOR (nome,
e-mail, telefone, grupo) vêm da identidade verificada — **nunca** do LLM. O
agente só decide o conteúdo (resumo/descrição) e a classificação
(categoria/ocorrência/prioridade), que o Go valida contra a config do SAC.

`telefone` e `conversation_id` são `InjectedToolArg`: ficam fora do schema
que o modelo vê e são preenchidos pelo `tools_node` a partir do estado da
sessão. O `conversation_id` (id da conversa no Chatwoot) vai em toda chamada
só para o gateway correlacionar seus logs de erro do SAC com a conversa.

As docstrings abaixo são lidas pelo LLM para escolher e usar a tool — edite com
intenção (elas custam tokens e guiam o comportamento).
"""

from __future__ import annotations

import logging
from typing import Annotated, Any, Optional

import httpx
from langchain_core.tools import InjectedToolArg, tool

from src.config import get_settings


logger = logging.getLogger(__name__)


def _post(path: str, payload: dict[str, Any]) -> dict[str, Any]:
    """POST no gateway Go. Falha de rede vira `{status: False, erro: ...}`."""
    settings = get_settings()
    url = settings.sac_tools_base_url.rstrip("/") + path
    headers = {"X-Tools-Token": settings.sac_tools_token}
    safe_payload = {k: v for k, v in payload.items() if k != "telefone"}
    logger.info("sac_tool_call path=%s payload=%s", path, safe_payload)
    try:
        resp = httpx.post(
            url, json=payload, headers=headers, timeout=settings.sac_tools_timeout
        )
        resp.raise_for_status()
        data = resp.json()
    except Exception as exc:  # noqa: BLE001 — devolvemos a falha pro agente decidir
        logger.warning("sac_tool_falhou path=%s erro=%s", path, exc)
        return {"status": False, "erro": "o sistema de chamados está indisponível agora"}
    logger.info("sac_tool_result path=%s status=%s", path, data.get("status"))
    return data


@tool
def consultar_tipos_de_chamado(
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
) -> dict[str, Any]:
    """Lista as categorias e ocorrências válidas para abrir um chamado no SAC.

    Chame ANTES de `abrir_chamado_suporte` para escolher a `categoria` e a
    `ocorrencia` que melhor descrevem o problema do cliente — só valores desta
    lista são aceitos na abertura. Cada item traz `categoria`, `ocorrencia`
    (o nome do tipo) e uma `descricao` que ajuda a casar o problema relatado.

    Returns:
        dict: status (bool), categorias (list[str]), ocorrencias (list de
        {categoria, ocorrencia, descricao, prazo}).
    """
    return _post(
        "/tools/sac/tipos",
        {"telefone": telefone, "conversation_id": conversation_id},
    )


@tool
def preparar_abertura_chamado(
    resumo: str,
    descricao: str,
    categoria: str,
    ocorrencia: str,
    prioridade: str = "MEDIA",
    empresa: str = "",
    novo_chamado: bool = False,
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
) -> dict[str, Any]:
    """Valida e prepara a abertura de um chamado (dry-run — NÃO cria nada).

    Chame ANTES de apresentar o resumo ao cliente: o sistema confere cada campo
    e devolve em `proposta` os valores canônicos que a abertura vai usar. Se
    vier motivo "ocorrencia_invalida", a resposta traz a lista `ocorrencias`
    válidas — escolha a que melhor casa e chame de novo AGORA, sem envolver o
    cliente. Se vier "empresa_ambigua", pergunte de qual das empresas DO
    CLIENTE é o chamado e repita passando `empresa`. Se vier "chamado_aberto",
    o cliente JÁ TEM chamado(s) em aberto (lista `chamados_abertos`, com
    protocolo, resumo e link): mostre-os e pergunte se o problema é de um
    deles — não abra outro por conta própria; só repita com
    `novo_chamado=true` se o cliente pedir explicitamente um chamado NOVO.
    "sem_email"/"sem_empresa": o cadastro não permite abrir por aqui — explique
    o motivo (está em `erro`) e ofereça um atendente.

    Com status true, apresente ao cliente o resumo do que será registrado (da
    `proposta`) e peça um "sim"/"pode abrir"; só na mensagem SEGUINTE dele, se
    for essa confirmação, chame `abrir_chamado_suporte`. A proposta vale 15
    minutos.

    Args:
        resumo: título curto do problema (1 linha). Será registrado em MAIÚSCULAS.
        descricao: descrição detalhada do que está acontecendo (preserva a caixa).
        categoria: categoria do problema (ex.: de `consultar_tipos_de_chamado`).
        ocorrencia: ocorrência dentro da categoria.
        prioridade: BAIXA | MEDIA | ALTA | URGENTE (default MEDIA). Use ALTA/
            URGENTE só quando o impacto for claramente alto (operação parada).
        empresa: informe SÓ quando o cliente tiver mais de uma empresa e o
            sistema pedir para desambiguar (motivo "empresa_ambigua").
        novo_chamado: True SÓ quando o cliente pediu explicitamente um chamado
            NOVO apesar de já ter chamado em aberto (motivo "chamado_aberto").
            Nunca decida isso por ele.

    Returns:
        dict: status (bool); em sucesso, proposta {resumo, descricao, categoria,
        ocorrencia, prioridade, empresa, prazo, novo_chamado}. Em falha, erro e
        às vezes motivo ("ocorrencia_invalida" com `ocorrencias` |
        "empresa_ambigua" com `empresas` | "chamado_aberto" com
        `chamados_abertos` | "campo_invalido" | "sem_email" | "sem_empresa" |
        "nao_identificado").
    """
    return _post(
        "/tools/sac/preparar",
        {
            "telefone": telefone,
            "conversation_id": conversation_id,
            "resumo": resumo,
            "descricao": descricao,
            "categoria": categoria,
            "ocorrencia": ocorrencia,
            "prioridade": prioridade,
            "grupo_emp": empresa,
            "novo_chamado": bool(novo_chamado),
        },
    )


@tool
def abrir_chamado_suporte(
    proposta: Annotated[Optional[dict], InjectedToolArg] = None,
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
) -> dict[str, Any]:
    """Abre o chamado da proposta JÁ PREPARADA e confirmada pelo cliente.

    AÇÃO COM EFEITO COLATERAL (LLM08): cria um chamado real. Só chame DEPOIS de
    `preparar_abertura_chamado` ter retornado status true E de o cliente
    confirmar explicitamente NA MENSAGEM SEGUINTE (só "pode abrir", "sim",
    "ok" — um protocolo, "esse", uma pergunta ou um pedido de mudança NÃO
    confirmam e o sistema recusa). Não recebe argumentos: o chamado criado é
    EXATAMENTE a proposta preparada — para mudar qualquer coisa (resumo,
    prioridade...), prepare de novo antes de abrir.

    Returns:
        dict: status (bool); em sucesso, protocolo, link (URL do chat do
        chamado) e prioridade. SEMPRE envie o `link` ao cliente e oriente-o a
        continuar a conversa pelo chat do chamado.
    """
    proposta = proposta or {}
    return _post(
        "/tools/sac/criar",
        {
            "telefone": telefone,
            "conversation_id": conversation_id,
            "resumo": proposta.get("resumo", ""),
            "descricao": proposta.get("descricao", ""),
            "categoria": proposta.get("categoria", ""),
            "ocorrencia": proposta.get("ocorrencia", ""),
            "prioridade": proposta.get("prioridade", "MEDIA"),
            "grupo_emp": proposta.get("empresa", ""),
            # Pedido explícito de chamado novo conferido no preparo (ZB-2): a
            # abertura o repete para o gateway reconferir os abertos.
            "novo_chamado": bool(proposta.get("novo_chamado", False)),
        },
    )


@tool
def listar_chamados_abertos(
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
) -> dict[str, Any]:
    """Lista os chamados EM ABERTO do cliente (pendentes / em andamento) no SAC.

    Use quando o cliente perguntar sobre seus chamados, tickets, protocolos ou
    o andamento de algo que reportou. A identidade já é da sessão — não peça
    nem invente identificadores. Ao responder, envie o `link` de cada chamado
    para o cliente acompanhar/conversar pelo chat do chamado.

    Returns:
        dict: status (bool), total (int), chamados (list de {protocolo, resumo,
        status, categoria, ocorrencia, dt_abertura, link}).
    """
    return _post(
        "/tools/sac/listar",
        {"telefone": telefone, "conversation_id": conversation_id},
    )


SAC_TOOLS = [
    consultar_tipos_de_chamado,
    preparar_abertura_chamado,
    abrir_chamado_suporte,
    listar_chamados_abertos,
]

# A injeção/validação de args de sessão agora é centralizada em
# `src/agent/tool_policy.py` (POLITICAS) — fonte única de verdade sobre o que
# o LLM pode fazer. Esta constante permanece só por compatibilidade.
TOOLS_COM_CONTEXTO_SESSAO = frozenset(t.name for t in SAC_TOOLS)
