"""CPF e contrato fora do histórico do grafo (regra 5 do CLAUDE.md).

O estado do grafo é persistido pelo checkpointer e aparece no trace. Por isso
nenhuma mensagem entra nele com CPF ou número de contrato em claro:

- `HumanMessage`: CPFs no texto viram `***.***.789-**`;
- `AIMessage.tool_calls[].args` (e o espelho cru em `additional_kwargs`):
  os args `cpf`/`contrato` viram a forma mascarada e CPFs em qualquer outro
  arg de texto também.

A máscara é feita no REDUTOR de `messages` (`state.add_messages_protegendo`, que
chama `proteger_mensagem` — instalado por `build_graph`), que
roda antes de o valor ser gravado em canal/checkpoint, inclusive para a
mensagem de entrada do turno. O valor em claro vive apenas no `_Cofre` abaixo —
um dicionário em memória do processo, com validade curta, que o turno consulta
por duas razões legítimas e só elas:

1. o LLM precisa LER o CPF que o cliente acabou de digitar para repassá-lo à
   tool (`reidratar` no `agent_node`);
2. a tool precisa EXECUTAR com o valor completo para falar com o gateway
   (`reidratar` no `tools_node`, antes de `aplicar_politica`).

O cofre é esvaziado no fim do turno (`descartar`) e expira sozinho (TTL) se o
turno for interrompido. Nada dele vai a disco.

Limite conhecido: a chamada ao LLM em si leva o valor em claro (o modelo tem de
lê-lo); o tracing das gerações é da observabilidade (E4.3).
"""

from __future__ import annotations

import json
import re
import threading
import time
import uuid
from collections.abc import Iterable, Mapping, Sequence
from typing import Any

from langchain_core.messages import AIMessage, BaseMessage, HumanMessage
from langgraph.checkpoint.base import BaseCheckpointSaver

from src.identity.contrato import mascarar_contrato
from src.identity.cpf import mascarar_cpf, somente_digitos

#: Validade do valor em claro no cofre: bem acima do prazo do turno
#: (`BRAIN_TURNO_PRAZO`), para nunca expirar no meio dele.
COFRE_TTL_S = 300.0
COFRE_MAX_ENTRADAS = 512

#: 11 dígitos, com ou sem a pontuação do CPF, sem dígito colado nas pontas.
#: Não exige dígito verificador: um CPF digitado errado também é PII.
_CPF_EM_TEXTO = re.compile(r"(?<!\d)\d{3}\.?\d{3}\.?\d{3}-?\d{2}(?!\d)")


def mascarar_cpfs_no_texto(texto: str) -> str:
    """Troca cada CPF encontrado em `texto` pela forma mascarada."""
    return _CPF_EM_TEXTO.sub(lambda m: mascarar_cpf(m.group(0)), texto)


# ---------------------------------------------------------------------------
# Máscara de valores (args e conteúdo)
# ---------------------------------------------------------------------------


def _mascarar_valor(chave: str | None, valor: Any) -> Any:
    if isinstance(valor, str):
        if chave == "cpf":
            # já mascarado (idempotência): `***.***.789-**` não tem 11 dígitos
            if "*" in valor and len(somente_digitos(valor)) != 11:
                return valor
            return mascarar_cpf(valor)
        if chave == "contrato":
            return mascarar_contrato(valor) if valor.strip() else valor
        return mascarar_cpfs_no_texto(valor)
    if isinstance(valor, Mapping):
        return {k: _mascarar_valor(str(k), v) for k, v in valor.items()}
    if isinstance(valor, list):
        return [_mascarar_valor(None, v) for v in valor]
    return valor


def mascarar_args(args: Mapping[str, Any] | None) -> dict[str, Any]:
    """Args de uma tool call sem CPF/contrato em claro."""
    return {k: _mascarar_valor(k, v) for k, v in (args or {}).items()}


def _mascarar_json(bruto: Any) -> Any:
    """`function.arguments` cru do provedor (string JSON) sem PII."""
    if not isinstance(bruto, str):
        return bruto
    try:
        carregado = json.loads(bruto)
    except ValueError:
        return mascarar_cpfs_no_texto(bruto)
    if not isinstance(carregado, dict):
        return mascarar_cpfs_no_texto(bruto)
    return json.dumps(mascarar_args(carregado), ensure_ascii=False)


def _mascarar_kwargs(kwargs: Mapping[str, Any]) -> dict[str, Any]:
    """`additional_kwargs["tool_calls"]` espelha os args no formato do provedor."""
    saida = dict(kwargs)
    brutas = saida.get("tool_calls")
    if isinstance(brutas, list):
        novas: list[Any] = []
        for item in brutas:
            if isinstance(item, Mapping) and isinstance(item.get("function"), Mapping):
                funcao = dict(item["function"])
                funcao["arguments"] = _mascarar_json(funcao.get("arguments"))
                item = {**item, "function": funcao}
            novas.append(item)
        saida["tool_calls"] = novas
    return saida


def _mascarar_conteudo(conteudo: Any) -> Any:
    """Texto da mensagem (str ou lista de blocos) sem CPF."""
    if isinstance(conteudo, str):
        return mascarar_cpfs_no_texto(conteudo)
    if isinstance(conteudo, list):
        blocos: list[Any] = []
        for bloco in conteudo:
            if isinstance(bloco, str):
                blocos.append(mascarar_cpfs_no_texto(bloco))
            elif isinstance(bloco, Mapping):
                novo = dict(bloco)
                if isinstance(novo.get("text"), str):
                    novo["text"] = mascarar_cpfs_no_texto(novo["text"])
                if isinstance(novo.get("input"), Mapping):  # bloco tool_use
                    novo["input"] = mascarar_args(novo["input"])
                blocos.append(novo)
            else:
                blocos.append(bloco)
        return blocos
    return conteudo


# ---------------------------------------------------------------------------
# Cofre em memória — o valor em claro, só durante o turno
# ---------------------------------------------------------------------------


class _Cofre:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._itens: dict[str, tuple[float, Any]] = {}

    def guardar(self, chave: str, valor: Any) -> None:
        agora = time.monotonic()
        with self._lock:
            self._expirar(agora)
            if len(self._itens) >= COFRE_MAX_ENTRADAS:
                # descarta o mais antigo; o turno dele já estourou o prazo há muito.
                mais_antiga = min(self._itens, key=lambda k: self._itens[k][0])
                del self._itens[mais_antiga]
            self._itens[chave] = (agora, valor)

    def obter(self, chave: str) -> Any | None:
        with self._lock:
            self._expirar(time.monotonic())
            item = self._itens.get(chave)
        return item[1] if item else None

    def remover(self, chave: str) -> None:
        with self._lock:
            self._itens.pop(chave, None)

    def limpar(self) -> None:
        with self._lock:
            self._itens.clear()

    def tamanho(self) -> int:
        with self._lock:
            return len(self._itens)

    def _expirar(self, agora: float) -> None:
        vencidas = [k for k, (t, _) in self._itens.items() if agora - t > COFRE_TTL_S]
        for k in vencidas:
            del self._itens[k]


_COFRE = _Cofre()


def _chave_texto(mensagem_id: str) -> str:
    return f"msg:{mensagem_id}"


def _chave_args(mensagem_id: str, indice: int) -> str:
    return f"args:{mensagem_id}:{indice}"


def limpar_cofre() -> None:
    """Esvazia o cofre (testes)."""
    _COFRE.limpar()


def tamanho_do_cofre() -> int:
    return _COFRE.tamanho()


# ---------------------------------------------------------------------------
# Proteger (antes do estado) e reidratar (durante o turno)
# ---------------------------------------------------------------------------


def _com_id(mensagem: BaseMessage) -> BaseMessage:
    if mensagem.id:
        return mensagem
    return mensagem.model_copy(update={"id": f"msg-{uuid.uuid4()}"})


def mascarar_mensagem(mensagem: BaseMessage) -> BaseMessage:
    """Cópia da mensagem sem PII (a mesma instância se não havia o que mascarar).

    Pura: não toca no cofre nem inventa `id`. Só `HumanMessage` (texto com CPF)
    e `AIMessage` (args de tool call e o espelho cru do provedor) são tocadas.
    Idempotente.
    """
    if isinstance(mensagem, HumanMessage):
        mascarado = _mascarar_conteudo(mensagem.content)
        if mascarado == mensagem.content:
            return mensagem
        return mensagem.model_copy(update={"content": mascarado})

    if isinstance(mensagem, AIMessage) and (mensagem.tool_calls or mensagem.additional_kwargs):
        chamadas: list[dict[str, Any]] = [dict(tc) for tc in mensagem.tool_calls or []]
        novas = [{**tc, "args": mascarar_args(tc.get("args"))} for tc in chamadas]
        kwargs = _mascarar_kwargs(mensagem.additional_kwargs)
        conteudo = (
            _mascarar_conteudo(mensagem.content)
            if isinstance(mensagem.content, list)
            else mensagem.content
        )
        if (
            novas == chamadas
            and kwargs == mensagem.additional_kwargs
            and conteudo == mensagem.content
        ):
            return mensagem
        return mensagem.model_copy(
            update={"tool_calls": novas, "additional_kwargs": kwargs, "content": conteudo}
        )

    return mensagem


def proteger_mensagem(mensagem: BaseMessage) -> BaseMessage:
    """Devolve a mensagem sem PII e guarda o original no cofre (por `id`).

    Mensagem sem PII devolve o MESMO objeto. Idempotente: uma mensagem já
    mascarada não muda nem vai ao cofre.
    """
    mascarada = mascarar_mensagem(mensagem)
    if mascarada is mensagem:
        return mensagem
    com_id = _com_id(mensagem)
    mensagem_id = com_id.id or ""
    if isinstance(mensagem, HumanMessage):
        _COFRE.guardar(_chave_texto(mensagem_id), mensagem.content)
    elif isinstance(mensagem, AIMessage):
        for i, (antes, depois) in enumerate(
            zip(mensagem.tool_calls, mascarada.tool_calls, strict=True)  # type: ignore[attr-defined]
        ):
            if antes.get("args") != depois.get("args"):
                _COFRE.guardar(_chave_args(mensagem_id, i), dict(antes.get("args") or {}))
    return mascarada.model_copy(update={"id": mensagem_id})


def reidratar(mensagens: Iterable[BaseMessage]) -> list[BaseMessage]:
    """Cópia das mensagens com o valor em claro do cofre — SÓ em memória.

    Para o LLM do turno ler o CPF digitado e para a tool executar com o dado
    completo. Quem chama não pode devolver o resultado ao estado.
    """
    saida: list[BaseMessage] = []
    for m in mensagens:
        if m.id and isinstance(m, HumanMessage):
            original = _COFRE.obter(_chave_texto(m.id))
            if original is not None:
                m = m.model_copy(update={"content": original})
        elif m.id and isinstance(m, AIMessage) and m.tool_calls:
            chamadas = [dict(tc) for tc in m.tool_calls]
            mudou = False
            for i, tc in enumerate(chamadas):
                original = _COFRE.obter(_chave_args(m.id, i))
                if original is not None:
                    tc["args"] = original
                    mudou = True
            if mudou:
                m = m.model_copy(update={"tool_calls": chamadas})
        saida.append(m)
    return saida


def descartar(mensagens: Iterable[BaseMessage]) -> None:
    """Fim do turno: apaga do cofre o que essas mensagens deixaram lá."""
    for m in mensagens:
        if not m.id:
            continue
        _COFRE.remover(_chave_texto(m.id))
        for i in range(len(getattr(m, "tool_calls", None) or [])):
            _COFRE.remover(_chave_args(m.id, i))


# ---------------------------------------------------------------------------
# Checkpointer: o que o LangGraph grava ANTES do redutor
# ---------------------------------------------------------------------------


def _mascarar_estrutura(valor: Any) -> Any:
    """Mascara mensagens em qualquer estrutura (dict/lista/tupla) a gravar."""
    if isinstance(valor, BaseMessage):
        return mascarar_mensagem(valor)
    if isinstance(valor, dict):
        return {k: _mascarar_estrutura(v) for k, v in valor.items()}
    if isinstance(valor, list):
        return [_mascarar_estrutura(v) for v in valor]
    if isinstance(valor, tuple) and not hasattr(valor, "_fields"):
        return tuple(_mascarar_estrutura(v) for v in valor)
    return valor


class CheckpointerMascarando(BaseCheckpointSaver):
    """Envolve um checkpointer e mascara PII em tudo o que ele grava.

    O redutor `add_messages_mascarando` protege o canal `messages`, mas o
    LangGraph também grava a ENTRADA bruta do turno (canal `__start__` e as
    escritas pendentes da tarefa de entrada) antes de o redutor rodar. Este
    envoltório passa checkpoints e escritas pela mesma máscara na porta do
    disco; leitura e demais operações são delegadas sem mudança.
    """

    def __init__(self, interno: BaseCheckpointSaver) -> None:
        super().__init__(serde=interno.serde)
        self.interno = interno

    # --- leitura: só delega --------------------------------------------------
    def get_tuple(self, config: Any) -> Any:
        return self.interno.get_tuple(config)

    async def aget_tuple(self, config: Any) -> Any:
        return await self.interno.aget_tuple(config)

    def list(self, config: Any, **kw: Any) -> Any:
        return self.interno.list(config, **kw)

    def alist(self, config: Any, **kw: Any) -> Any:
        return self.interno.alist(config, **kw)

    def get_next_version(self, current: Any, channel: Any) -> Any:
        return self.interno.get_next_version(current, channel)

    def delete_thread(self, thread_id: str) -> None:
        self.interno.delete_thread(thread_id)

    async def adelete_thread(self, thread_id: str) -> None:
        await self.interno.adelete_thread(thread_id)

    # --- escrita: mascara antes de gravar -----------------------------------
    @staticmethod
    def _checkpoint(checkpoint: Any) -> Any:
        valores = checkpoint.get("channel_values")
        if not valores:
            return checkpoint
        return {**checkpoint, "channel_values": _mascarar_estrutura(valores)}

    @staticmethod
    def _writes(writes: Sequence[tuple[str, Any]]) -> Sequence[tuple[str, Any]]:
        return [(canal, _mascarar_estrutura(valor)) for canal, valor in writes]

    def put(self, config: Any, checkpoint: Any, metadata: Any, new_versions: Any) -> Any:
        return self.interno.put(
            config, self._checkpoint(checkpoint), _mascarar_estrutura(metadata), new_versions
        )

    async def aput(self, config: Any, checkpoint: Any, metadata: Any, new_versions: Any) -> Any:
        return await self.interno.aput(
            config, self._checkpoint(checkpoint), _mascarar_estrutura(metadata), new_versions
        )

    def put_writes(
        self, config: Any, writes: Sequence[tuple[str, Any]], task_id: str, task_path: str = ""
    ) -> None:
        self.interno.put_writes(config, self._writes(writes), task_id, task_path)

    async def aput_writes(
        self, config: Any, writes: Sequence[tuple[str, Any]], task_id: str, task_path: str = ""
    ) -> None:
        await self.interno.aput_writes(config, self._writes(writes), task_id, task_path)

    def __getattr__(self, nome: str) -> Any:
        # Só chamado quando o atributo NÃO existe aqui: tudo o mais é do interno.
        return getattr(self.__dict__["interno"], nome)
