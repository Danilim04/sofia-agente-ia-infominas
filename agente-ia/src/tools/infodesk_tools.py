"""As 6 tools do Infodesk expostas ao modelo (`docs/plano/01-ferramentas.md`, seção A).

Regras que valem para todas (`CLAUDE.md`, regras 1–5):

- A tool **nunca fala com o Infodesk**: só com o gateway Go, em
  `POST /tools/infodesk/<área>/<ação>` com o header `X-Tools-Token`. Autorização,
  posse, idempotência e taxonomia moram lá e na política (`tool_policy.py`).
- Argumentos de sessão (`telefone`, `conversation_id`, `classificacao`,
  `proposta`, `identificacao`) são `InjectedToolArg`: ficam FORA do schema que
  o modelo vê e são preenchidos do estado por `aplicar_politica`.
- A docstring é a instrução ao modelo (vira a `description` da tool): quando
  chamar, o que fazer com cada `motivo` de recusa. Sem segredo, sem nome de
  mesa/departamento/prioridade.
- Erro de negócio chega do gateway como HTTP 200 `{status: false, erro, motivo}`.
  Qualquer outra coisa (timeout, 5xx, corpo que não é JSON) vira o erro
  genérico `infodesk_indisponivel` — texto cru de erro nunca chega ao modelo.
- Efeito colateral em duas fases: `preparar_abertura_chamado` (dry-run) →
  confirmação explícita no turno seguinte (checada por código) →
  `abrir_chamado` SEM argumentos do modelo. Nenhuma tool repete um POST.
- PII: CPF/contrato em claro só na memória do turno e no corpo para o gateway;
  logs e retorno só levam a forma mascarada.
"""

from __future__ import annotations

import logging
from collections.abc import Mapping
from typing import Annotated, Any, Literal

import httpx
from langchain_core.tools import InjectedToolArg, tool

from src.config import get_settings
from src.identity.contrato import mascarar_contrato, normalizar_contrato
from src.identity.cpf import mascarar_cpf, somente_digitos
from src.tools.validacao import chave_idempotencia, normalizar_numero_chamado

logger = logging.getLogger(__name__)

_BASE = "/tools/infodesk"

#: O `abrir` do gateway tem orçamento de 25 s até o POST/reconciliação do ticket
#: (`toolsapi/abrir.go: abrirTeto`) e mais ≤ 10 s de folga própria para o handoff
#: final (`posTeto`). Se a tool desistisse antes, o cliente ouviria "não consegui"
#: de um chamado que o gateway ainda estava criando. O teto do turno do cérebro
#: (`BRAIN_TURNO_PRAZO`, 40 s) fica acima deste valor.
ABRIR_ORCAMENTO_GATEWAY_S = 25.0
ABRIR_FOLGA_POS_S = 10.0
ABRIR_TIMEOUT_MINIMO_S = ABRIR_ORCAMENTO_GATEWAY_S + ABRIR_FOLGA_POS_S

_ERRO_INDISPONIVEL = (
    "não consegui consultar o sistema agora; peça desculpa, diga que houve uma "
    "instabilidade e ofereça um atendente — não invente dados"
)
_ERRO_ABRIR_INCERTO = (
    "não consegui confirmar se o chamado foi registrado; NÃO tente abrir outro e "
    "não diga que foi aberto — diga que um atendente vai conferir e assumir"
)


def _indisponivel(erro: str = _ERRO_INDISPONIVEL) -> dict[str, Any]:
    return {"status": False, "erro": erro, "motivo": "infodesk_indisponivel"}


def _post(
    caminho: str,
    payload: Mapping[str, Any],
    *,
    timeout: float | None = None,
    erro_indisponivel: str = _ERRO_INDISPONIVEL,
) -> dict[str, Any]:
    """`POST <gateway>/tools/infodesk/<caminho>` com `X-Tools-Token`.

    Devolve o JSON do gateway quando é um envelope `{status, ...}`; em qualquer
    falha de transporte/protocolo devolve `infodesk_indisponivel`. **Sem retry**:
    um POST com efeito colateral (`ticket/abrir`) nunca é repetido pelo cliente —
    a idempotência é do gateway, por chave (ADR 0005).
    """
    cfg = get_settings()
    base = cfg.infodesk_tools_base_url.rstrip("/")
    url = f"{base}{_BASE}/{caminho}"
    headers = {"X-Tools-Token": cfg.infodesk_tools_token, "Content-Type": "application/json"}
    espera = timeout if timeout is not None else cfg.infodesk_tools_timeout
    try:
        resp = httpx.post(url, json=dict(payload), headers=headers, timeout=espera)
    except httpx.TimeoutException:
        logger.warning("gateway_tool_timeout caminho=%s timeout=%ss", caminho, espera)
        return _indisponivel(erro_indisponivel)
    except httpx.HTTPError as exc:
        logger.warning("gateway_tool_rede caminho=%s erro=%s", caminho, type(exc).__name__)
        return _indisponivel(erro_indisponivel)
    if resp.status_code != 200:
        # 401 = token errado (configuração); 400/405 = bug de protocolo; 5xx = gateway.
        logger.error("gateway_tool_http caminho=%s status=%s", caminho, resp.status_code)
        return _indisponivel(erro_indisponivel)
    try:
        dados = resp.json()
    except ValueError:
        logger.error("gateway_tool_corpo_invalido caminho=%s", caminho)
        return _indisponivel(erro_indisponivel)
    if not isinstance(dados, dict) or not isinstance(dados.get("status"), bool):
        logger.error("gateway_tool_envelope_invalido caminho=%s", caminho)
        return _indisponivel(erro_indisponivel)
    if not dados["status"]:
        logger.info("gateway_tool_recusa caminho=%s motivo=%s", caminho, dados.get("motivo"))
    return dados


# ---------------------------------------------------------------------------
# A1 — identificação (fallback do lookup por telefone)
# ---------------------------------------------------------------------------


@tool
def informar_identificacao(
    cpf: str = "",
    contrato: str = "",
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
) -> dict[str, Any]:
    """Registra o CPF ou o número do contrato que o cliente acabou de informar.

    Use SÓ quando o bloco "Cliente desta conversa" disser que a identificação NÃO
    foi informada e o cliente já tiver dito o CPF ou o contrato (peça um dos dois
    antes, explicando que é para registrar o chamado). Passe o dado exatamente
    como o cliente escreveu; nunca corrija, complete nem invente dígitos. Se o
    cliente já está identificado, NÃO chame esta ferramenta.

    O sistema valida o dado. Se a ferramenta recusar como inválido, peça com
    simpatia para o cliente conferir e mandar de novo. Se vier "nao_identificado",
    peça o CPF ou o número do contrato. Quando der certo, siga o atendimento sem
    pedir o dado de novo e sem repeti-lo na conversa.

    Args:
        cpf: CPF informado pelo cliente (11 dígitos, com ou sem pontuação).
        contrato: número do contrato informado pelo cliente (alternativa ao CPF).

    Returns:
        dict: status (bool); em sucesso identificado=True e forma ("cpf" ou
        "contrato"); em recusa erro (texto para você explicar) e motivo.
    """
    cpf_digitos = somente_digitos(cpf)
    contrato_norm = normalizar_contrato(contrato)
    if cpf_digitos:
        logger.info("informar_identificacao forma=cpf valor=%s", mascarar_cpf(cpf_digitos))
    elif contrato_norm:
        logger.info(
            "informar_identificacao forma=contrato valor=%s", mascarar_contrato(contrato_norm)
        )
    return _post(
        "contato/identificar",
        {
            "telefone": telefone,
            "conversation_id": conversation_id,
            "cpf": cpf_digitos,
            "contrato": contrato_norm,
        },
    )


# ---------------------------------------------------------------------------
# A2/A3 — chamado em duas fases
# ---------------------------------------------------------------------------


@tool
def preparar_abertura_chamado(
    titulo: str,
    descricao: str,
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
    classificacao: Annotated[dict | None, InjectedToolArg] = None,
    identificacao: Annotated[dict | None, InjectedToolArg] = None,
) -> dict[str, Any]:
    """Ensaio da abertura de chamado: valida e devolve a proposta. NÃO cria nada.

    Chame depois de entender o problema e de o cliente estar identificado, e SEMPRE
    antes de mostrar o resumo ao cliente. Depois de preparar, mostre o resumo
    (título e descrição, em linguagem natural) e peça um "sim"; só então, na
    PRÓXIMA mensagem do cliente, `abrir_chamado` pode ser chamado. Se o cliente
    pedir qualquer mudança, prepare de novo.

    Trate o retorno pelo "motivo":
    - "chamado_aberto": o cliente JÁ tem chamado aberto sobre isso; mostre o número
      (em "chamados_abertos") e pergunte se é o mesmo problema. Não abra outro por
      conta própria.
    - "nao_identificado": peça o CPF ou o número do contrato e registre com
      `informar_identificacao`.
    - "campo_invalido": ajuste título (até 120 caracteres) e descrição (até 4000).
    - "categoria_sem_mapa" ou "referencia_indisponivel": explique que não dá para
      abrir por aqui agora e ofereça um atendente.

    Args:
        titulo: resumo do problema em UMA linha (até 120 caracteres).
        descricao: relato do cliente em 3ª pessoa + fatos coletados (desde quando,
            se já reiniciou o equipamento, endereço se citado), até 4000 caracteres.

    Returns:
        dict: status (bool); em sucesso proposta (titulo, descricao e resumo do
        que será registrado); em recusa erro e motivo.
    """
    classificacao = classificacao or {}
    return _post(
        "ticket/preparar",
        {
            "telefone": telefone,
            "conversation_id": conversation_id,
            "titulo": titulo,
            "descricao": descricao,
            "categoria": classificacao.get("categoria") or "",
            "prioridade": classificacao.get("prioridade") or "",
            "identificacao": identificacao,
        },
    )


@tool
def abrir_chamado(
    proposta: Annotated[dict | None, InjectedToolArg] = None,
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
) -> dict[str, Any]:
    """Abre o chamado EXATAMENTE como foi preparado e confirmado. Sem argumentos.

    Só chame depois de `preparar_abertura_chamado` numa mensagem anterior E de o
    cliente ter confirmado com um "sim"/"pode abrir" na mensagem seguinte ao
    resumo. Nunca chame na mesma rodada em que preparou. O sistema recusa a
    abertura se a confirmação não for explícita ou a proposta tiver vencido
    (15 minutos); nesse caso responda ao que o cliente disse e, se for preciso,
    prepare de novo.

    Trate o retorno pelo "motivo":
    - sucesso: informe o NÚMERO do chamado ("numero") e diga que a equipe
      responsável assume a partir dali; não prometa prazo. Se "transferido" for
      verdadeiro, avise que a conversa segue com um atendente.
    - "em_andamento": o chamado já está sendo aberto; aguarde, não abra outro.
    - "infodesk_indisponivel" ou "escopo_insuficiente": não foi possível abrir por
      aqui; diga que um atendente vai assumir. Não prometa prazo nem tente de novo.
    - "sem_proposta": prepare o chamado de novo antes de abrir.

    Returns:
        dict: status (bool); em sucesso numero e transferido; em recusa erro e motivo.
    """
    proposta = dict(proposta or {})
    if not proposta.get("criada_em"):
        return {
            "status": False,
            "erro": "não há proposta de chamado preparada; prepare o chamado antes de abrir",
            "motivo": "sem_proposta",
        }
    # `turno` é carimbo de controle do cérebro; o contrato leva a proposta + `criada_em`.
    proposta.pop("turno", None)
    return _post(
        "ticket/abrir",
        {
            "telefone": telefone,
            "conversation_id": conversation_id,
            "proposta": proposta,
            "chave_idempotencia": chave_idempotencia(conversation_id, proposta),
        },
        timeout=max(get_settings().infodesk_tools_timeout, ABRIR_TIMEOUT_MINIMO_S),
        erro_indisponivel=_ERRO_ABRIR_INCERTO,
    )


# ---------------------------------------------------------------------------
# A4/A5 — consulta
# ---------------------------------------------------------------------------


@tool
def consultar_status_chamado(
    numero: str,
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
    identificacao: Annotated[dict | None, InjectedToolArg] = None,
) -> dict[str, Any]:
    """Consulta o andamento de um chamado pelo número que o cliente informou.

    Use quando o cliente perguntar como está um chamado e já tiver dito o número
    (se não disse, peça). Um número citado é de um chamado EXISTENTE: nunca vire
    isso em chamado novo. Repasse o "status_ticket" exatamente como veio; nunca
    invente status, etapa ou prazo.

    Trate o retorno pelo "motivo":
    - "posse_nao_verificada": diga que não consegue confirmar esse chamado por aqui
      e ofereça um atendente; não diga nada sobre o chamado.
    - "nao_encontrado": peça para o cliente conferir o número.
    - "infodesk_indisponivel": peça para tentar mais tarde ou ofereça um atendente.

    Args:
        numero: número do chamado, só dígitos.

    Returns:
        dict: status (bool); em sucesso numero, status_ticket, titulo, aberto_em;
        em recusa erro e motivo.
    """
    return _post(
        "ticket/consultar",
        {
            "telefone": telefone,
            "conversation_id": conversation_id,
            "numero": normalizar_numero_chamado(numero) or "",
            "identificacao": identificacao,
        },
    )


@tool
def listar_meus_chamados(
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
) -> dict[str, Any]:
    """Lista os chamados do cliente desta conversa. Sem argumentos.

    Use quando o cliente perguntar quais chamados ele tem. A lista traz os chamados
    abertos no Infodesk mais os abertos por aqui; repita ao cliente o texto de
    "observacao" para ele saber o que a lista cobre e o "status_ticket" de cada um
    como veio. Se "total" for 0, diga que não encontrou chamados e ofereça abrir um.
    Se vier "chamados_indisponiveis", peça para tentar mais tarde ou ofereça um
    atendente.

    Returns:
        dict: status (bool); em sucesso total, chamados (numero, titulo, categoria,
        status_ticket, aberto_em) e observacao; em recusa erro e motivo.
    """
    return _post(
        "ticket/listar",
        {"telefone": telefone, "conversation_id": conversation_id},
    )


# ---------------------------------------------------------------------------
# A6 — transferência a pedido do modelo
# ---------------------------------------------------------------------------


@tool
def transferir_para_atendente(
    motivo: Literal["pedido_cliente", "fora_do_escopo", "nao_resolvido"],
    telefone: Annotated[str, InjectedToolArg] = "",
    conversation_id: Annotated[str, InjectedToolArg] = "",
    classificacao: Annotated[dict | None, InjectedToolArg] = None,
) -> dict[str, Any]:
    """Passa a conversa para um atendente humano.

    Use quando o cliente pedir para falar com uma pessoa ("pedido_cliente"), quando
    o assunto fugir do que você consegue resolver ("fora_do_escopo") ou quando você
    já tentou e o problema não foi resolvido ("nao_resolvido"). O pedido do cliente
    já é a confirmação: não peça autorização. O sistema escolhe o departamento e
    envia o aviso formal ao cliente; você só confirma, em UMA frase, que está
    passando a conversa — e para de conduzir o atendimento.

    Trate o retorno pelo "motivo":
    - "ja_transferida": a conversa já está com um atendente; não responda mais.
    - "infodesk_indisponivel": diga que um atendente vai assumir a conversa.

    Args:
        motivo: "pedido_cliente", "fora_do_escopo" ou "nao_resolvido".

    Returns:
        dict: status (bool); em sucesso departamento e mensagem_ao_cliente; em
        recusa erro e motivo.
    """
    # O contrato (`conversa/transferir/request.json`) não leva o telefone: o
    # gateway resolve a conversa pelo `conversation_id`.
    payload: dict[str, Any] = {"conversation_id": conversation_id, "motivo": motivo}
    categoria = (classificacao or {}).get("categoria")
    if categoria:
        payload["categoria"] = categoria
    return _post("conversa/transferir", payload)


#: Ordem do catálogo (`docs/plano/01-ferramentas.md`, A1–A6).
TOOLS_INFODESK = [
    informar_identificacao,
    preparar_abertura_chamado,
    abrir_chamado,
    consultar_status_chamado,
    listar_meus_chamados,
    transferir_para_atendente,
]
