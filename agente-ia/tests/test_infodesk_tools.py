"""As 6 tools do Infodesk com o gateway falso (`httpx.post` monkeypatchado).

Cada tool é exercitada com os JSON de `contratos/tools-infodesk/`: o payload que
ela envia tem de ser IGUAL ao `request.json` e cada resposta (`ok` e todo
`erro-<motivo>`) chega ao modelo exatamente como o gateway mandou. Falhas de
transporte viram o erro genérico `infodesk_indisponivel` — texto cru nunca passa.
"""

from __future__ import annotations

import copy
import hashlib
import json
from pathlib import Path

import httpx
import pytest
from langchain_core.utils.function_calling import convert_to_openai_tool

from src.agent.graph import get_default_tools
from src.tools import infodesk_tools
from src.tools.infodesk_tools import (
    ABRIR_TIMEOUT_MINIMO_S,
    abrir_chamado,
    consultar_status_chamado,
    informar_identificacao,
    listar_meus_chamados,
    preparar_abertura_chamado,
    transferir_para_atendente,
)
from tests.fakes import (
    GATEWAY_TOKEN_DE_TESTE,
    RAIZ_CONTRATOS_TOOLS,
    contrato_tools,
    instalar_gateway_fake,
)

ARGS_DE_SESSAO = {"telefone", "conversation_id", "classificacao", "proposta", "identificacao"}
_SESSAO = {
    "telefone": "+5531999990001",
    "conversation_id": "6f1c2a3e-9b4d-4e7a-8c21-5d3f0a9b7e10",
}


def _motivos(caminho: str) -> list[str]:
    """Todo `erro-<motivo>.json` do endpoint."""
    arquivos = sorted((RAIZ_CONTRATOS_TOOLS / caminho).glob("erro-*.json"))
    return [a.stem.removeprefix("erro-") for a in arquivos]


# ---------------------------------------------------------------------------
# Catálogo e schema que o modelo vê
# ---------------------------------------------------------------------------


def test_catalogo_tem_as_6_tools_do_plano_na_ordem():
    assert [t.name for t in get_default_tools()] == [
        "informar_identificacao",
        "preparar_abertura_chamado",
        "abrir_chamado",
        "consultar_status_chamado",
        "listar_meus_chamados",
        "transferir_para_atendente",
    ]


@pytest.mark.parametrize("tool", get_default_tools(), ids=lambda t: t.name)
def test_nenhum_arg_de_sessao_no_schema_enviado_ao_modelo(tool):
    """O schema que o `bind_tools` manda ao LLM (formato OpenAI) não leva arg de sessão."""
    schema = convert_to_openai_tool(tool)["function"]
    propriedades = set(schema["parameters"].get("properties", {}))
    obrigatorios = set(schema["parameters"].get("required", []))
    assert not propriedades & ARGS_DE_SESSAO, f"{tool.name} expõe {propriedades & ARGS_DE_SESSAO}"
    assert not obrigatorios & ARGS_DE_SESSAO
    # e nem o texto da descrição ensina o modelo a passar esses args
    assert "InjectedToolArg" not in json.dumps(schema)


def test_args_que_o_modelo_ve_sao_os_do_plano():
    esperado = {
        "informar_identificacao": {"cpf", "contrato"},
        "preparar_abertura_chamado": {"titulo", "descricao"},
        "abrir_chamado": set(),
        "consultar_status_chamado": {"numero"},
        "listar_meus_chamados": set(),
        "transferir_para_atendente": {"motivo"},
    }
    for tool in get_default_tools():
        schema = convert_to_openai_tool(tool)["function"]["parameters"]
        assert set(schema.get("properties", {})) == esperado[tool.name], tool.name


def test_transferir_expoe_o_motivo_como_enum():
    schema = convert_to_openai_tool(transferir_para_atendente)["function"]["parameters"]
    assert schema["properties"]["motivo"]["enum"] == [
        "pedido_cliente",
        "fora_do_escopo",
        "nao_resolvido",
    ]


def test_docstrings_ensinam_o_modelo_a_tratar_cada_motivo_de_recusa():
    """A docstring é a instrução ao LLM: todo `motivo` que o contrato pode devolver
    e que exige uma atitude do modelo aparece na descrição da tool."""
    por_tool = {
        "preparar_abertura_chamado": "ticket/preparar",
        "abrir_chamado": "ticket/abrir",
        "consultar_status_chamado": "ticket/consultar",
        "listar_meus_chamados": "ticket/listar",
        "transferir_para_atendente": "conversa/transferir",
    }
    tools = {t.name: t for t in get_default_tools()}
    for nome, caminho in por_tool.items():
        descricao = tools[nome].description
        for motivo in _motivos(caminho):
            assert motivo in descricao, f"{nome}: a docstring não trata '{motivo}'"


# ---------------------------------------------------------------------------
# informar_identificacao
# ---------------------------------------------------------------------------


def test_informar_identificacao_envia_o_payload_do_contrato(monkeypatch):
    gw = instalar_gateway_fake(
        monkeypatch, {"contato/identificar": contrato_tools("contato/identificar", "ok")}
    )
    out = informar_identificacao.invoke({"cpf": "123.456.789-09", **_SESSAO})
    assert out == contrato_tools("contato/identificar", "ok")
    [chamada] = gw.chamadas
    assert chamada["json"] == contrato_tools("contato/identificar", "request")
    assert chamada["headers"]["X-Tools-Token"] == GATEWAY_TOKEN_DE_TESTE


def test_informar_identificacao_por_contrato(monkeypatch):
    gw = instalar_gateway_fake(
        monkeypatch,
        {
            "contato/identificar": {
                **contrato_tools("contato/identificar", "ok"),
                "forma": "contrato",
            }
        },
    )
    informar_identificacao.invoke({"contrato": " 000123456 ", **_SESSAO})
    assert gw.chamadas[0]["json"] == {**_SESSAO, "cpf": "", "contrato": "000123456"}


@pytest.mark.parametrize("motivo", _motivos("contato/identificar"))
def test_informar_identificacao_devolve_cada_erro_do_contrato(monkeypatch, motivo):
    esperado = contrato_tools("contato/identificar", f"erro-{motivo}")
    instalar_gateway_fake(monkeypatch, {"contato/identificar": esperado})
    assert informar_identificacao.invoke({"cpf": "12345678909", **_SESSAO}) == esperado


def test_informar_identificacao_nao_loga_o_cpf_em_claro(monkeypatch, caplog):
    instalar_gateway_fake(
        monkeypatch, {"contato/identificar": contrato_tools("contato/identificar", "ok")}
    )
    with caplog.at_level("DEBUG"):
        informar_identificacao.invoke({"cpf": "123.456.789-09", **_SESSAO})
    assert "12345678909" not in caplog.text and "123.456.789-09" not in caplog.text
    assert "***.***.789-**" in caplog.text


# ---------------------------------------------------------------------------
# preparar_abertura_chamado
# ---------------------------------------------------------------------------

_REQ_PREPARAR = contrato_tools("ticket/preparar", "request")
_CLASSIFICACAO = {
    "intencao": "reclamacao_problema",
    "categoria": "sem_conexao",
    "prioridade": "alta",
    "confianca": 0.88,
    "origem": "jev",
}


def _args_preparar() -> dict:
    return {
        "titulo": _REQ_PREPARAR["titulo"],
        "descricao": _REQ_PREPARAR["descricao"],
        "classificacao": _CLASSIFICACAO,
        "identificacao": _REQ_PREPARAR["identificacao"],
        **_SESSAO,
    }


def test_preparar_envia_o_payload_do_contrato_e_devolve_a_proposta(monkeypatch):
    ok = contrato_tools("ticket/preparar", "ok")
    gw = instalar_gateway_fake(monkeypatch, {"ticket/preparar": ok})
    assert preparar_abertura_chamado.invoke(_args_preparar()) == ok
    assert gw.chamadas[0]["json"] == _REQ_PREPARAR


@pytest.mark.parametrize("motivo", _motivos("ticket/preparar"))
def test_preparar_devolve_cada_erro_do_contrato(monkeypatch, motivo):
    esperado = contrato_tools("ticket/preparar", f"erro-{motivo}")
    instalar_gateway_fake(monkeypatch, {"ticket/preparar": esperado})
    assert preparar_abertura_chamado.invoke(_args_preparar()) == esperado


def test_preparar_sem_classificacao_nem_identificacao_nao_quebra(monkeypatch):
    gw = instalar_gateway_fake(
        monkeypatch,
        {"ticket/preparar": contrato_tools("ticket/preparar", "erro-nao_identificado")},
    )
    preparar_abertura_chamado.invoke({"titulo": "t", "descricao": "d", **_SESSAO})
    enviado = gw.chamadas[0]["json"]
    assert enviado["categoria"] == "" and enviado["prioridade"] == ""
    assert enviado["identificacao"] is None


# ---------------------------------------------------------------------------
# abrir_chamado — sem args do LLM; chave de idempotência da proposta carimbada
# ---------------------------------------------------------------------------

_REQ_ABRIR = contrato_tools("ticket/abrir", "request")


def _proposta_carimbada() -> dict:
    """A proposta do contrato com o carimbo de turno que o `efeitos` acrescenta."""
    return {**copy.deepcopy(_REQ_ABRIR["proposta"]), "turno": 1}


def test_abrir_chamado_nao_tem_argumento_do_llm():
    schema = convert_to_openai_tool(abrir_chamado)["function"]["parameters"]
    assert schema.get("properties", {}) == {}


def test_abrir_envia_o_payload_do_contrato_com_a_chave_do_adr_0005(monkeypatch):
    ok = contrato_tools("ticket/abrir", "ok")
    gw = instalar_gateway_fake(monkeypatch, {"ticket/abrir": ok})
    out = abrir_chamado.invoke({"proposta": _proposta_carimbada(), **_SESSAO})
    assert out == ok
    # IGUAL ao request.json: a proposta sem o `turno` e a chave sha256 real do arquivo
    assert gw.chamadas[0]["json"] == _REQ_ABRIR
    p = _REQ_ABRIR["proposta"]
    bruto = _REQ_ABRIR["conversation_id"] + p["titulo"] + p["categoria"] + p["criada_em"]
    assert _REQ_ABRIR["chave_idempotencia"] == hashlib.sha256(bruto.encode()).hexdigest()


def test_chave_muda_quando_a_proposta_muda(monkeypatch):
    gw = instalar_gateway_fake(monkeypatch, {"ticket/abrir": contrato_tools("ticket/abrir", "ok")})
    outra = _proposta_carimbada()
    outra["criada_em"] = "2026-10-20T14:40:00-03:00"
    abrir_chamado.invoke({"proposta": _proposta_carimbada(), **_SESSAO})
    abrir_chamado.invoke({"proposta": outra, **_SESSAO})
    chaves = [c["json"]["chave_idempotencia"] for c in gw.chamadas]
    assert chaves[0] != chaves[1]


def test_abrir_com_a_mesma_proposta_repete_a_mesma_chave(monkeypatch):
    gw = instalar_gateway_fake(monkeypatch, {"ticket/abrir": contrato_tools("ticket/abrir", "ok")})
    for _ in range(2):
        abrir_chamado.invoke({"proposta": _proposta_carimbada(), **_SESSAO})
    assert (
        gw.chamadas[0]["json"]["chave_idempotencia"] == gw.chamadas[1]["json"]["chave_idempotencia"]
    )


@pytest.mark.parametrize("motivo", _motivos("ticket/abrir"))
def test_abrir_devolve_cada_erro_do_contrato(monkeypatch, motivo):
    esperado = contrato_tools("ticket/abrir", f"erro-{motivo}")
    instalar_gateway_fake(monkeypatch, {"ticket/abrir": esperado})
    assert abrir_chamado.invoke({"proposta": _proposta_carimbada(), **_SESSAO}) == esperado


@pytest.mark.parametrize("proposta", [None, {}, {"titulo": "x", "categoria": "y"}])
def test_abrir_sem_proposta_carimbada_nem_chama_o_gateway(monkeypatch, proposta):
    gw = instalar_gateway_fake(monkeypatch, {})
    out = abrir_chamado.invoke({"proposta": proposta, **_SESSAO})
    assert out["status"] is False and out["motivo"] == "sem_proposta"
    assert gw.chamadas == []


def test_abrir_espera_mais_que_o_orcamento_de_25s_do_gateway(monkeypatch):
    """Se a tool desistisse antes do gateway, o cliente ouviria 'não consegui' de
    um chamado que ainda estava sendo criado."""
    gw = instalar_gateway_fake(monkeypatch, {"ticket/abrir": contrato_tools("ticket/abrir", "ok")})
    abrir_chamado.invoke({"proposta": _proposta_carimbada(), **_SESSAO})
    assert gw.chamadas[0]["timeout"] >= ABRIR_TIMEOUT_MINIMO_S > 25


# ---------------------------------------------------------------------------
# consultar_status_chamado / listar_meus_chamados
# ---------------------------------------------------------------------------


def test_consultar_envia_o_payload_do_contrato(monkeypatch):
    ok = contrato_tools("ticket/consultar", "ok")
    gw = instalar_gateway_fake(monkeypatch, {"ticket/consultar": ok})
    req = contrato_tools("ticket/consultar", "request")
    out = consultar_status_chamado.invoke(
        {"numero": "48213", "identificacao": req["identificacao"], **_SESSAO}
    )
    assert out == ok
    assert gw.chamadas[0]["json"] == req


@pytest.mark.parametrize("bruto", ["48213", " 48213 ", "#48213"])
def test_consultar_normaliza_o_numero(monkeypatch, bruto):
    gw = instalar_gateway_fake(
        monkeypatch, {"ticket/consultar": contrato_tools("ticket/consultar", "ok")}
    )
    consultar_status_chamado.invoke({"numero": bruto, **_SESSAO})
    assert gw.chamadas[0]["json"]["numero"] == "48213"


@pytest.mark.parametrize("motivo", _motivos("ticket/consultar"))
def test_consultar_devolve_cada_erro_do_contrato(monkeypatch, motivo):
    esperado = contrato_tools("ticket/consultar", f"erro-{motivo}")
    instalar_gateway_fake(monkeypatch, {"ticket/consultar": esperado})
    assert consultar_status_chamado.invoke({"numero": "48213", **_SESSAO}) == esperado


def test_listar_envia_o_payload_do_contrato(monkeypatch):
    ok = contrato_tools("ticket/listar", "ok")
    gw = instalar_gateway_fake(monkeypatch, {"ticket/listar": ok})
    assert listar_meus_chamados.invoke(_SESSAO) == ok
    assert gw.chamadas[0]["json"] == contrato_tools("ticket/listar", "request")
    assert gw.chamadas[0]["timeout"] == 30.0  # timeout das settings


@pytest.mark.parametrize("motivo", _motivos("ticket/listar"))
def test_listar_devolve_cada_erro_do_contrato(monkeypatch, motivo):
    esperado = contrato_tools("ticket/listar", f"erro-{motivo}")
    instalar_gateway_fake(monkeypatch, {"ticket/listar": esperado})
    assert listar_meus_chamados.invoke(_SESSAO) == esperado


# ---------------------------------------------------------------------------
# transferir_para_atendente
# ---------------------------------------------------------------------------


def test_transferir_envia_o_payload_do_contrato(monkeypatch):
    ok = contrato_tools("conversa/transferir", "ok")
    gw = instalar_gateway_fake(monkeypatch, {"conversa/transferir": ok})
    out = transferir_para_atendente.invoke(
        {"motivo": "pedido_cliente", "classificacao": _CLASSIFICACAO, **_SESSAO}
    )
    assert out == ok
    assert gw.chamadas[0]["json"] == contrato_tools("conversa/transferir", "request")


def test_transferir_sem_categoria_omite_a_chave(monkeypatch):
    gw = instalar_gateway_fake(
        monkeypatch, {"conversa/transferir": contrato_tools("conversa/transferir", "ok")}
    )
    transferir_para_atendente.invoke({"motivo": "nao_resolvido", **_SESSAO})
    assert gw.chamadas[0]["json"] == {
        "conversation_id": _SESSAO["conversation_id"],
        "motivo": "nao_resolvido",
    }


@pytest.mark.parametrize("motivo", _motivos("conversa/transferir"))
def test_transferir_devolve_cada_erro_do_contrato(monkeypatch, motivo):
    esperado = contrato_tools("conversa/transferir", f"erro-{motivo}")
    instalar_gateway_fake(monkeypatch, {"conversa/transferir": esperado})
    assert transferir_para_atendente.invoke({"motivo": "pedido_cliente", **_SESSAO}) == esperado


# ---------------------------------------------------------------------------
# _post — falhas de transporte e de protocolo nunca chegam cruas ao modelo
# ---------------------------------------------------------------------------

_FALHAS = {
    "timeout": httpx.ReadTimeout("lento"),
    "conexao": httpx.ConnectError("recusada em 10.0.0.7"),
    "http_500": (500, "Traceback: senha=123 em /srv/app.py"),
    "http_401": (401, "unauthorized"),
    "http_400": (400, "bad json"),
    "corpo_nao_json": (200, "<html>erro interno</html>"),
    "json_sem_envelope": (200, '{"x": 1}'),
    "json_lista": (200, "[1, 2]"),
}


@pytest.mark.parametrize("falha", list(_FALHAS))
def test_falha_de_transporte_vira_infodesk_indisponivel_sem_texto_cru(monkeypatch, falha):
    instalar_gateway_fake(monkeypatch, {"ticket/listar": _FALHAS[falha]})
    out = listar_meus_chamados.invoke(_SESSAO)
    assert out["status"] is False and out["motivo"] == "infodesk_indisponivel"
    texto = json.dumps(out, ensure_ascii=False)
    for vazamento in ("Traceback", "senha", "10.0.0.7", "unauthorized", "html", "lento"):
        assert vazamento not in texto


def test_timeout_do_abrir_diz_ao_modelo_para_nao_abrir_outro(monkeypatch):
    """Sem resposta o chamado pode ter sido criado: nada de 'abri' nem de segundo POST."""
    gw = instalar_gateway_fake(monkeypatch, {"ticket/abrir": httpx.ReadTimeout("x")})
    out = abrir_chamado.invoke({"proposta": _proposta_carimbada(), **_SESSAO})
    assert out["motivo"] == "infodesk_indisponivel"
    assert "NÃO tente abrir outro" in out["erro"]
    assert len(gw.chamadas) == 1  # nunca repete um POST com efeito colateral


@pytest.mark.parametrize("falha", ["timeout", "conexao", "http_500"])
def test_nenhuma_tool_repete_o_post_quando_falha(monkeypatch, falha):
    gw = instalar_gateway_fake(
        monkeypatch,
        {c: _FALHAS[falha] for c in ("ticket/preparar", "ticket/abrir", "conversa/transferir")},
    )
    preparar_abertura_chamado.invoke(_args_preparar())
    abrir_chamado.invoke({"proposta": _proposta_carimbada(), **_SESSAO})
    transferir_para_atendente.invoke({"motivo": "pedido_cliente", **_SESSAO})
    assert [c["caminho"] for c in gw.chamadas] == [
        "ticket/preparar",
        "ticket/abrir",
        "conversa/transferir",
    ]


def test_post_manda_o_token_e_usa_a_base_das_settings(monkeypatch):
    gw = instalar_gateway_fake(
        monkeypatch, {"ticket/listar": contrato_tools("ticket/listar", "ok")}
    )
    listar_meus_chamados.invoke(_SESSAO)
    assert gw.chamadas[0]["headers"]["X-Tools-Token"] == GATEWAY_TOKEN_DE_TESTE
    assert gw.chamadas[0]["headers"]["Content-Type"] == "application/json"


def test_tools_nao_falam_com_o_infodesk():
    """Só o gateway: nenhum domínio/rota do Infodesk no módulo das tools."""
    fonte = Path(infodesk_tools.__file__).read_text(encoding="utf-8")
    assert "/integration/v1" not in fonte
    assert "infominas.com" not in fonte.lower()
