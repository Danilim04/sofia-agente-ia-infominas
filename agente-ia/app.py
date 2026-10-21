"""Entry point Chainlit — harness de dev da Sofia (sem gateway, sem Infodesk).

Como rodar:

    chainlit run app.py -w          # ou: scripts/start-all.sh --chainlit

Pré-requisitos:
- `.env` com `OPENROUTER_API_KEY` real.
- `python -m src.rag.ingest` já executado (ChromaDB populado), se quiser RAG.
- Para as tools do Infodesk (identificar, abrir chamado, transferir) o gateway
  precisa estar no ar (`scripts/start-all.sh`) e `INFODESK_TOOLS_BASE_URL` /
  `INFODESK_TOOLS_TOKEN` apontarem para ele (`scripts/dev.env`).

Simula o gateway Go falando com o cérebro pelo **Contrato A** completo
(`contratos/contrato-a/chat-request.json` → `processar_chat` →
`chat-response.json`), no mesmo processo:

1. `on_chat_start` pede o **telefone** e o **nome** do cliente (o que o gateway
   extrai de `data.contact` do webhook do Infodesk) e pergunta se o contato já
   chega **identificado** (`contato.forma = "telefone"`, como o lookup do
   gateway manda) ou **não identificado** (`contato = null`: a Sofia pede
   CPF/contrato na conversa).
2. Cada mensagem vira um `ChatRequest` (conversation_id novo por sessão,
   `estado_conversa = "bot"`) e passa por `server.processar_chat`. A resposta
   chega como o WhatsApp a mostraria: uma mensagem por bolha (`---`).
3. Depois de cada turno um **passo** do Chainlit mostra o que o gateway
   receberia além do texto: `classificacao`, `acoes`, `fontes` e `erro`. O
   painel mascara o telefone (`****0001`); o que o operador digita nas
   perguntas iniciais continua visível para ele, como em qualquer chat.
4. Uma ação `transferir` encerra a vez da Sofia (o gateway "cala" o bot): as
   mensagens seguintes não vão ao cérebro até `/encerrar` ou `/trocar-telefone`.

Comandos: `/trocar-telefone [<numero>]` (outro cliente na mesma sessão) e
`/encerrar` (simula `conversation.finished`: `POST /encerrar-episodio`).

Sem streaming de tokens: o Contrato A devolve a resposta inteira, e o harness
mostra o mesmo que o gateway veria.

Limites (o gateway real faz mais): o `conversation_id` do harness não existe no
store do gateway, então `consultar_chamado`/`listar_chamados` (que exigem a
conversa lá) não funcionam por aqui — use a UI do mock em `:9000` para o fluxo
completo de tools; e `erro` só aparece no painel (o gateway repetiria falha
transitória e transferiria na definitiva). TODO(E4.1): o E2E cobre o fluxo
completo pelo gateway.
"""

from __future__ import annotations

import logging
import re
import uuid
from typing import Any

import chainlit as cl

import server
from src.agent.prompts import NOME_ASSISTENTE

logger = logging.getLogger(__name__)


COMANDO_TROCAR_TELEFONE = "/trocar-telefone"
COMANDO_ENCERRAR = "/encerrar"

#: Marcador de bolha do Contrato A: linha só com `---` (o gateway envia cada
#: bolha como uma mensagem de WhatsApp).
_SEPARADOR_BOLHA = re.compile(r"^\s*---\s*$", re.MULTILINE)

#: Ações do cliente na pergunta de identificação do contato.
ACAO_NAO_IDENTIFICADO = "nao_identificado"
ACAO_IDENTIFICADO_TELEFONE = "identificado_telefone"


# ---------------------------------------------------------------------------
# Grafo — o mesmo do `server.py` (compilado uma vez por processo). O
# isolamento entre conversas é o `conversation_id` (= `thread_id`).
# ---------------------------------------------------------------------------

_GRAPH: Any = None


def _get_graph():
    global _GRAPH
    if _GRAPH is None:
        _GRAPH = server._get_graph()
    return _GRAPH


# ---------------------------------------------------------------------------
# Helpers puros (testáveis sem chainlit)
# ---------------------------------------------------------------------------


def normalizar_telefone(telefone: Any) -> str:
    """Só dígitos (aceita máscara `(31) 99999-0001`); vazio se não houver."""
    return re.sub(r"\D", "", str(telefone or ""))


def telefone_e164(telefone: Any) -> str:
    """Telefone como o Infodesk o entrega ao gateway: `+` e dígitos (E.164)."""
    digitos = normalizar_telefone(telefone)
    return f"+{digitos}" if digitos else ""


def mascarar_telefone(telefone: Any) -> str:
    """`****0001`: o mesmo formato do gateway (`mascararTelefone`). Tudo que o
    harness mostra nos painéis passa por aqui (o que o operador digita é dele)."""
    digitos = normalizar_telefone(telefone)
    if len(digitos) <= 4:
        return "****"
    return "****" + digitos[-4:]


def abreviar_nome(nome: Any) -> str:
    """`Maria Souza` → `Maria S.`: o `valor_mascarado` da forma `telefone`
    (mesma regra do gateway, `abreviarNome`)."""
    partes = str(nome or "").split()
    if not partes:
        return ""
    if len(partes) == 1:
        return partes[0]
    inicial = partes[-1][0]
    if not inicial.isalpha():
        return partes[0]
    return f"{partes[0]} {inicial.upper()}."


def montar_contato_telefone(nome_cadastro: Any) -> dict[str, Any]:
    """`contato` de um telefone identificado pelo lookup do gateway (`forma =
    "telefone"`): `valor_mascarado` é o nome abreviado; `cliente_id` fica nulo
    (não há cadastro real no harness)."""
    nome = str(nome_cadastro or "").strip()
    return {
        "forma": "telefone",
        "valor_mascarado": abreviar_nome(nome),
        "cliente_id": None,
        "nome_cadastro": nome or None,
    }


def montar_contato_dev(telefone: Any, nome: Any, identificado: bool = False) -> dict[str, Any]:
    """O que o gateway mandaria no Contrato A para este cliente.

    `identificado=False` (default): sem identificação em cache — `contato` é
    `None` e o agente pede CPF/contrato. `identificado=True`: o lookup do
    gateway reconheceu o telefone (`forma = "telefone"`).

    Devolve `{"telefone", "nome_contato", "contato"}` prontos para a sessão.
    """
    nome_limpo = str(nome or "").strip() or None
    return {
        "telefone": normalizar_telefone(telefone),
        "nome_contato": nome_limpo,
        "contato": montar_contato_telefone(nome_limpo) if identificado else None,
    }


def montar_chat_request(
    *,
    conversation_id: str,
    mensagem: str,
    telefone: Any,
    nome_contato: str | None = None,
    contato: dict[str, Any] | None = None,
    canal: str = "whatsapp",
) -> dict[str, Any]:
    """Corpo do `POST /chat` (Contrato A) para um turno do cliente.

    Segue `contratos/contrato-a/chat-request.json`: `telefone` em E.164,
    `nome_contato` só quando existe (o gateway o omite vazio), `contato`
    `null` ou o objeto de 4 chaves e `estado_conversa` sempre `"bot"` (o
    gateway nunca manda turno de conversa transferida).
    """
    req: dict[str, Any] = {
        "conversation_id": conversation_id,
        "canal": canal,
        "mensagem": mensagem,
        "telefone": telefone_e164(telefone),
    }
    if nome_contato:
        req["nome_contato"] = nome_contato
    req["contato"] = dict(contato) if contato else None
    req["estado_conversa"] = "bot"
    return req


def dividir_bolhas(reply: Any) -> list[str]:
    """Quebra a resposta nas bolhas marcadas com `---` (uma mensagem de
    WhatsApp cada). Resposta vazia → nenhuma bolha."""
    return [b.strip() for b in _SEPARADOR_BOLHA.split(str(reply or "")) if b.strip()]


def saudacao_dev(nome_contato: Any, identificado: bool = False) -> str:
    """Mensagem de abertura do harness (NÃO é a saudação da Sofia — essa vem do
    grafo). Só confirma quem está simulado na sessão."""
    nome = abreviar_nome(nome_contato) or "cliente"
    if identificado:
        ident = (
            "Contato **identificado** pelo telefone (`contato.forma = telefone`): "
            f"a {NOME_ASSISTENTE} já o trata como cliente reconhecido."
        )
    else:
        ident = (
            f"Sem identificação em cache (`contato = null`) — a {NOME_ASSISTENTE} vai "
            "pedir CPF ou contrato quando precisar."
        )
    return (
        f"Sessão de dev: você é **{nome}**, cliente final falando com a "
        f"{NOME_ASSISTENTE} pelo WhatsApp. {ident} "
        f"Use `{COMANDO_TROCAR_TELEFONE}` para simular outro cliente e "
        f"`{COMANDO_ENCERRAR}` para simular o fim da conversa."
    )


def extrair_texto_resposta_ask(res: Any) -> str:
    """Lê o texto digitado em resposta a `cl.AskUserMessage`.

    Aceita `dict` (formato recente do Chainlit) ou objeto com `.output`/`.content`.
    """
    if res is None:
        return ""
    if isinstance(res, dict):
        valor = res.get("output") or res.get("content") or ""
    else:
        valor = getattr(res, "output", None) or getattr(res, "content", None) or ""
    return (valor or "").strip()


def extrair_acao_escolhida(res: Any) -> str:
    """Lê o `name` da ação escolhida em `cl.AskActionMessage` (dict, objeto ou
    `None` se o tempo esgotou)."""
    if res is None:
        return ""
    if isinstance(res, dict):
        return str(res.get("name") or res.get("value") or "")
    return str(getattr(res, "name", None) or getattr(res, "value", None) or "")


def formatar_fontes(fontes: Any) -> str | None:
    """Monta o markdown de citação. URLs viram links, restantes ficam em backticks."""
    if not fontes:
        return None
    rotulos: list[str] = []
    for f in fontes:
        if isinstance(f, str) and f.lower().startswith(("http://", "https://")):
            rotulos.append(f"🌐 [{f}]({f})")
        else:
            rotulos.append(f"📄 `{f}`")
    return "**Fontes consultadas:** " + " · ".join(rotulos)


def _celula(valor: Any) -> str:
    if valor in (None, ""):
        return "—"
    return " ".join(str(valor).split()).replace(
        "|", "\\|"
    )  # sem quebra de linha: não racha a tabela


def formatar_classificacao(classificacao: Any) -> str:
    """Tabela markdown das 5 chaves de `classificacao` (ou aviso quando `null`)."""
    if not classificacao:
        return "**classificacao:** `null` (nenhuma fixada ainda para esta conversa)"
    linhas = ["**classificacao**", "", "| campo | valor |", "|---|---|"]
    chaves = ("intencao", "categoria", "prioridade", "confianca", "origem")
    for chave in chaves:
        linhas.append(f"| {chave} | {_celula(classificacao.get(chave))} |")
    extras = [k for k in classificacao if k not in chaves]
    for chave in extras:  # o contrato só cresce: campo novo aparece sem mudar o harness
        linhas.append(f"| {chave} | {_celula(classificacao.get(chave))} |")
    return "\n".join(linhas)


def formatar_acoes(acoes: Any) -> str:
    """Lista markdown das `acoes` que o gateway executaria (ou `[]`)."""
    if not acoes:
        return "**acoes:** `[]`"
    linhas = ["**acoes** (o gateway executa)", ""]
    for a in acoes:
        extras = {k: v for k, v in a.items() if k not in ("tipo", "origem", "motivo")}
        resto = f" · {extras}" if extras else ""
        linhas.append(
            f"- `{_celula(a.get('tipo'))}` · origem `{_celula(a.get('origem'))}` — "
            f"{_celula(a.get('motivo'))}{resto}"
        )
    return "\n".join(linhas)


def formatar_erro(erro: Any) -> str | None:
    """Bloco do envelope `erro` (classe, id da ocorrência e causa); `None` sem erro."""
    if not erro:
        return None
    return (
        f"**erro:** classe `{_celula(erro.get('classe'))}` · "
        f"ocorrência `{_celula(erro.get('id_ocorrencia'))}` — {_celula(erro.get('causa'))}"
    )


def formatar_contato(contato: Any) -> str:
    if not contato:
        return "`null` (não identificado)"
    return (
        f"`forma={_celula(contato.get('forma'))}` · "
        f"`valor_mascarado={_celula(contato.get('valor_mascarado'))}`"
    )


def formatar_painel(
    resposta: Any,
    *,
    telefone: Any,
    conversation_id: str,
    contato: Any = None,
) -> str:
    """Markdown do passo "Contrato A" de um turno: o que o gateway recebe além
    do texto — `classificacao`, `acoes`, `fontes`, `erro` — mais um resumo do
    pedido com o telefone MASCARADO. `resposta` é o `ChatResponse` como dict
    (`chat-response.json`)."""
    resposta = resposta or {}
    partes = [
        f"**pedido:** telefone `{mascarar_telefone(telefone)}` · "
        f"conversation_id `{conversation_id}` · contato {formatar_contato(contato)}",
        formatar_classificacao(resposta.get("classificacao")),
        formatar_acoes(resposta.get("acoes")),
    ]
    fontes = formatar_fontes(resposta.get("fontes") or [])
    if fontes:
        partes.append(fontes)
    erro = formatar_erro(resposta.get("erro"))
    if erro:
        partes.append(erro)
    return "\n\n".join(partes)


def exige_transferencia(resposta: Any) -> bool:
    """O turno trouxe uma ação `transferir`? (o gateway passaria a conversa a um
    atendente e calaria a Sofia)."""
    return any((a or {}).get("tipo") == "transferir" for a in (resposta or {}).get("acoes") or [])


# ---------------------------------------------------------------------------
# Perguntas ao "cliente" (telefone, nome, identificação)
# ---------------------------------------------------------------------------


async def _perguntar(texto: str) -> str:
    res = await cl.AskUserMessage(content=texto, timeout=300).send()
    return extrair_texto_resposta_ask(res)


async def _pedir_telefone() -> str:
    return await _perguntar(
        "Informe o **telefone** do cliente (simula o `data.contact.phone` do "
        "webhook do Infodesk). Ex.: `5531990000100` (Maria Souza no mock-infodesk) "
        "ou `+55 (31) 99000-0100` — com DDI `55`, como o Infodesk entrega."
    )


async def _pedir_nome() -> str:
    return await _perguntar(
        "Informe o **nome** do cliente como aparece no WhatsApp (simula o "
        "`data.contact.name`). Ex.: `Maria Souza`."
    )


async def _pedir_identificacao() -> bool:
    """`True` se o contato chega identificado pelo telefone (`forma = telefone`)."""
    res = await cl.AskActionMessage(
        content=(
            "O gateway já identifica este telefone (lookup do Infodesk)? "
            "Isso muda o `contato` do Contrato A."
        ),
        actions=[
            cl.Action(
                name=ACAO_NAO_IDENTIFICADO,
                payload={"identificado": False},
                label="Não identificado (contato = null)",
            ),
            cl.Action(
                name=ACAO_IDENTIFICADO_TELEFONE,
                payload={"identificado": True},
                label="Identificado pelo telefone (forma = telefone)",
            ),
        ],
        timeout=300,
    ).send()
    return extrair_acao_escolhida(res) == ACAO_IDENTIFICADO_TELEFONE


async def _iniciar_sessao(telefone: str) -> None:
    nome = await _pedir_nome()
    identificado = await _pedir_identificacao()
    dados = montar_contato_dev(telefone, nome, identificado=identificado)
    cl.user_session.set("telefone", dados["telefone"])
    cl.user_session.set("nome_contato", dados["nome_contato"])
    cl.user_session.set("contato", dados["contato"])
    cl.user_session.set("conversation_id", str(uuid.uuid4()))
    cl.user_session.set("transferida", False)
    await cl.Message(content=saudacao_dev(dados["nome_contato"], identificado)).send()


# ---------------------------------------------------------------------------
# Handlers Chainlit
# ---------------------------------------------------------------------------


@cl.on_chat_start
async def on_chat_start():
    telefone = normalizar_telefone(await _pedir_telefone())
    if not telefone:
        await cl.Message(
            content=(
                "Telefone não informado. Envie qualquer mensagem para tentar "
                f"novamente ou use `{COMANDO_TROCAR_TELEFONE} <numero>`."
            ),
        ).send()
        return
    await _iniciar_sessao(telefone)


async def _encerrar_episodio() -> None:
    """Simula `conversation.finished`: o gateway avisa o cérebro, que zera
    proposta, classificação e identificação do episódio (o histórico fica)."""
    conversation_id = cl.user_session.get("conversation_id")
    if not conversation_id:
        await cl.Message(content="Sessão sem conversa iniciada.").send()
        return
    res = await server.encerrar_episodio(_get_graph(), conversation_id)
    cl.user_session.set("transferida", False)
    campos = ", ".join(f"`{c}`" for c in res.campos_limpos) or "nada a limpar"
    await cl.Message(
        content=f"Episódio encerrado (`POST /encerrar-episodio`): {campos}. A Sofia volta a atender."
    ).send()


async def _enviar_bolhas(reply: str) -> None:
    for bolha in dividir_bolhas(reply):
        await cl.Message(content=bolha).send()


@cl.on_message
async def on_message(message: cl.Message):
    texto = (message.content or "").strip()

    # /trocar-telefone [<numero>]
    if texto.lower().startswith(COMANDO_TROCAR_TELEFONE.lower()):
        resto = texto[len(COMANDO_TROCAR_TELEFONE) :].strip()
        novo = normalizar_telefone(resto or await _pedir_telefone())
        if not novo:
            await cl.Message(content="Nenhum telefone informado.").send()
            return
        await _iniciar_sessao(novo)
        return

    if texto.lower() == COMANDO_ENCERRAR:
        await _encerrar_episodio()
        return

    telefone = cl.user_session.get("telefone")
    if not telefone:
        await cl.Message(
            content=(
                "Sessão sem telefone identificado. "
                f"Use `{COMANDO_TROCAR_TELEFONE} <numero>` para iniciar."
            ),
        ).send()
        return

    if cl.user_session.get("transferida"):
        await cl.Message(
            content=(
                "Conversa transferida a um atendente (simulado): o gateway não manda "
                f"mais turnos ao cérebro. Use `{COMANDO_ENCERRAR}` para a Sofia voltar "
                f"ou `{COMANDO_TROCAR_TELEFONE}` para outro cliente."
            ),
        ).send()
        return

    conversation_id = cl.user_session.get("conversation_id")
    contato = cl.user_session.get("contato")
    pedido = montar_chat_request(
        conversation_id=conversation_id,
        mensagem=texto,
        telefone=telefone,
        nome_contato=cl.user_session.get("nome_contato"),
        contato=contato,
    )

    try:
        resposta = await server.processar_chat(_get_graph(), server.ChatRequest(**pedido))
    except Exception as exc:
        # processar_chat já converte falha do grafo em `erro`; isto é bug do harness.
        logger.exception("falha_no_harness")
        await cl.Message(content=f"Erro no harness: {type(exc).__name__}").send()
        return

    dados = resposta.model_dump()
    await _enviar_bolhas(dados["reply"])

    async with cl.Step(name="Contrato A · classificação e ações", type="tool") as passo:
        passo.output = formatar_painel(
            dados, telefone=telefone, conversation_id=conversation_id, contato=contato
        )

    if exige_transferencia(dados):
        cl.user_session.set("transferida", True)
        await cl.Message(
            content="Ação `transferir`: o gateway passaria a conversa a um atendente (simulado)."
        ).send()
