"""Anonimizador: cada tipo de PII, com e sem máscara, e a CLI ponta a ponta (sem rede)."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from evals import anonimizar as an
from evals.anonimizar import Anonimizador

EXEMPLOS = Path(__file__).resolve().parent.parent / "exemplos"


@pytest.fixture
def anon() -> Anonimizador:
    return Anonimizador(nomes=["Maria Souza", "Maria", "João da Silva"])


def test_cpf_valido_dv():
    assert an.cpf_valido("52998224725")
    assert an.cpf_valido("12345678909")
    assert not an.cpf_valido("52998224726")
    assert not an.cpf_valido("11111111111")
    assert not an.cpf_valido("31999990001")  # celular de BH, DV não bate
    assert not an.cpf_valido("123")


@pytest.mark.parametrize(
    ("texto", "esperado"),
    [
        ("cpf 529.982.247-25", "cpf <CPF>"),
        ("cpf 52998224725", "cpf <CPF>"),
        ("cpf 529982247-25", "cpf <CPF>"),
        ("cpf:12345678909.", "cpf:<CPF>."),
        ("cpf 529 982 247 25", "cpf <CPF>"),
        ("cpf 529.982.247.25", "cpf <CPF>"),
        ("cpf 529982247 25", "cpf <CPF>"),
        ("cnpj 12.345.678/0001-95", "cnpj <CNPJ>"),
        ("cnpj 12345678000195", "cnpj <CNPJ>"),
        ("cnpj 12345678/0001-95", "cnpj <CNPJ>"),
        ("liga +55 31 99999-0001", "liga <TEL>"),
        ("liga +5531999990001", "liga <TEL>"),
        ("liga 5531999990001", "liga <TEL>"),
        ("liga (31) 99999-0001", "liga <TEL>"),
        ("liga (31)99999-0001", "liga <TEL>"),
        ("liga 31 98888 7777", "liga <TEL>"),
        ("liga 31999990001", "liga <TEL>"),  # 11 dígitos, DV inválido, DDD+9 → celular
        ("liga 3133334444", "liga <TEL>"),  # fixo com DDD
        ("liga (31) 3333-4444", "liga <TEL>"),
        ("liga 99999-0001", "liga <TEL>"),
        ("liga 3333-4444", "liga <TEL>"),
        ("liga 3333.4444", "liga <TEL>"),
        ("mail x.y+z@mail.com.br ok", "mail <EMAIL> ok"),
        ("mail JOAO_1@Exemplo.COM", "mail <EMAIL>"),
        ("cep 30130-010", "cep <CEP>"),
        ("cep 30130010", "cep <CEP>"),
        ("cep 30.130-010", "cep <CEP>"),
        ("placa ABC-1234", "placa <PLACA>"),
        ("placa ABC1234", "placa <PLACA>"),
        ("placa ABC 1234", "placa <PLACA>"),  # espaço só em maiúsculas
        ("placa ABC1D23", "placa <PLACA>"),
        ("placa abc1d23", "placa <PLACA>"),
        ("placa ABC-1D23", "placa <PLACA>"),
        ("placa ABC 1D23", "placa <PLACA>"),
        ("Maria e MARIA SOUZA e joão da silva", "<NOME> e <NOME> e <NOME>"),
    ],
)
def test_substitui(anon: Anonimizador, texto: str, esperado: str):
    assert anon.texto(texto) == esperado


@pytest.mark.parametrize(
    "texto",
    [
        "protocolo 4821",
        "dia 20/10/2026 às 14:30",
        "valor R$ 1234,56",
        "plano de 500 mega por 99,90",
        "Mariana e Marianinha",  # não são "Maria" inteira
        "contrato 100000000000",  # 12 dígitos: nem CPF nem telefone
        "senha wifi abcd1234",  # 4 letras: não é placa
        "tem 1000 reais de multa",  # minúsculas + espaço + 4 dígitos: não é placa
        "a luz do modem ta vermelha",  # "Luz" não está na lista de nomes do teste
    ],
)
def test_nao_toca_no_que_nao_e_pii(anon: Anonimizador, texto: str):
    assert anon.texto(texto) == texto


def test_telefone_com_espacos_nao_vira_cpf(anon: Anonimizador):
    assert anon.texto("liga 31 99999 0001 ou 99999 0001") == "liga <TEL> ou <TEL>"
    assert anon.contagem["<CPF>"] == 0


def test_idempotente_em_texto_ja_mascarado(anon: Anonimizador):
    t = "cpf <CPF> tel <TEL> email <EMAIL> <NOME>"
    assert anon.texto(t) == t


def test_mistura_e_relatorio(anon: Anonimizador):
    saida = anon.texto(
        "Maria, cpf 529.982.247-25, tel (31) 99999-0001, cep 30130-010, email a@b.co, placa ABC1D23, cnpj 12345678000195"
    )
    assert (
        saida
        == "<NOME>, cpf <CPF>, tel <TEL>, cep <CEP>, email <EMAIL>, placa <PLACA>, cnpj <CNPJ>"
    )
    assert anon.contagem == {
        "<NOME>": 1,
        "<CPF>": 1,
        "<TEL>": 1,
        "<CEP>": 1,
        "<EMAIL>": 1,
        "<PLACA>": 1,
        "<CNPJ>": 1,
    }
    rel = anon.relatorio()
    for token in an.TOKENS:
        assert token in rel
    assert "total         7" in rel


def test_sem_lista_de_nomes_nao_mascara_nomes():
    a = Anonimizador()
    assert a.texto("Maria ligou") == "Maria ligou"


def test_valor_recursivo(anon: Anonimizador):
    dado = {
        "contato": {"nome": "Maria", "tel": "31999990001"},
        "msgs": ["cpf 52998224725", 7, None],
    }
    assert anon.valor(dado) == {
        "contato": {"nome": "<NOME>", "tel": "<TEL>"},
        "msgs": ["cpf <CPF>", 7, None],
    }


def test_carregar_nomes_ignora_comentarios(tmp_path: Path):
    arq = tmp_path / "nomes.txt"
    arq.write_text("# comentário\nMaria\n\n  João  \n", encoding="utf-8")
    assert an.carregar_nomes(arq) == ["Maria", "João"]
    assert an.carregar_nomes(None) == []
    assert an.carregar_nomes(tmp_path / "nao-existe.txt") == []


def test_ler_registros_csv_json_jsonl(tmp_path: Path):
    csv_regs = an.ler_registros(EXEMPLOS / "export-exemplo.csv")
    assert len(csv_regs) == 6 and set(csv_regs[0]) == {"conversa_id", "data", "contato", "mensagem"}
    json_regs = an.ler_registros(EXEMPLOS / "export-exemplo.json")
    assert [r["id"] for r in json_regs] == ["c-001", "c-004"]  # pegou a única lista do objeto
    jl = tmp_path / "x.jsonl"
    jl.write_text('{"a": 1}\n\n{"a": 2}\n', encoding="utf-8")
    assert an.ler_registros(jl) == [{"a": 1}, {"a": 2}]
    with pytest.raises(ValueError, match="extensão"):
        an.ler_registros(tmp_path / "x.xlsx")


def test_montar_pre_caso():
    pre = an.montar_pre_caso(3, {"mensagem": "linha 1\n\nlinha 2", "data": "x"}, "mensagem")
    assert pre == {
        "id": "hist-0003",
        "fonte": "historico",
        "estado": ["linha 1", "linha 2"],
        "esperado": None,
        "rotulador": None,
        "origem": {"data": "x"},
    }
    with pytest.raises(KeyError, match="coluna 'texto'"):
        an.montar_pre_caso(1, {"mensagem": "x"}, "texto")


def test_cli_csv_ponta_a_ponta(tmp_path: Path):
    saida = tmp_path / "out" / "historico.jsonl"
    linhas: list[str] = []
    codigo = an.main(
        [
            str(EXEMPLOS / "export-exemplo.csv"),
            "--saida",
            str(saida),
            "--nomes",
            str(EXEMPLOS / "nomes.txt"),
            "--amostra",
            "2",
        ],
        saida_texto=linhas.append,
    )
    assert codigo == 0
    regs = [json.loads(linha) for linha in saida.read_text(encoding="utf-8").splitlines()]
    assert len(regs) == 6
    texto_total = json.dumps(regs, ensure_ascii=False)
    for pii in (
        "529.982.247-25",
        "52998224725",
        "99999-0001",
        "exemplo.com.br",
        "30130-010",
        "ABC1D23",
        "31988887777",
        "12.345.678/0001-95",
        "Maria",
        "Pedro",
    ):
        assert pii not in texto_total, pii
    assert (
        regs[0]["mensagem"]
        == "bom dia, aqui é a <NOME>, meu cpf é <CPF> e a internet caiu desde ontem"
    )
    assert regs[0]["contato"] == "<NOME>"
    assert regs[5]["mensagem"] == "vcs cobraram duas vezes, protocolo 4821, cpf <CPF>"
    saida_txt = "\n".join(linhas)
    assert "6 registro(s)" in saida_txt
    assert "amostra de 2" in saida_txt
    assert "REVISÃO MANUAL OBRIGATÓRIA" in saida_txt
    assert "<CPF>" in saida_txt and "<PLACA>" in saida_txt


def test_cli_coluna_texto_gera_pre_caso(tmp_path: Path):
    saida = tmp_path / "pre.jsonl"
    codigo = an.main(
        [
            str(EXEMPLOS / "export-exemplo.csv"),
            "--saida",
            str(saida),
            "--coluna-texto",
            "mensagem",
            "--nomes",
            str(EXEMPLOS / "nomes.txt"),
        ],
        saida_texto=lambda _: None,
    )
    assert codigo == 0
    regs = [json.loads(linha) for linha in saida.read_text(encoding="utf-8").splitlines()]
    assert regs[0]["id"] == "hist-0001" and regs[0]["fonte"] == "historico"
    assert regs[0]["estado"] == [
        "bom dia, aqui é a <NOME>, meu cpf é <CPF> e a internet caiu desde ontem"
    ]
    assert regs[0]["esperado"] is None and regs[0]["origem"]["contato"] == "<NOME>"


def test_cli_json(tmp_path: Path):
    saida = tmp_path / "j.jsonl"
    assert (
        an.main(
            [
                str(EXEMPLOS / "export-exemplo.json"),
                "--saida",
                str(saida),
                "--nomes",
                str(EXEMPLOS / "nomes.txt"),
            ],
            saida_texto=lambda _: None,
        )
        == 0
    )
    regs = [json.loads(linha) for linha in saida.read_text(encoding="utf-8").splitlines()]
    assert regs[0]["contato"] == {"nome": "<NOME>", "telefone": "<TEL>", "email": "<EMAIL>"}
    assert regs[1]["contato"]["telefone"] == "<TEL>"
    assert regs[1]["mensagens"] == ["o tecnico veio com o carro placa <PLACA> e nao resolveu nada"]


def test_cli_erros(tmp_path: Path):
    msgs: list[str] = []
    assert (
        an.main(
            [str(tmp_path / "nao.csv"), "--saida", str(tmp_path / "o.jsonl")],
            saida_texto=msgs.append,
        )
        == 2
    )
    assert "não existe" in msgs[-1]
    ruim = tmp_path / "ruim.txt"
    ruim.write_text("x", encoding="utf-8")
    assert an.main([str(ruim), "--saida", str(tmp_path / "o.jsonl")], saida_texto=msgs.append) == 2
    assert "extensão" in msgs[-1]
    assert (
        an.main(
            [
                str(EXEMPLOS / "export-exemplo.csv"),
                "--saida",
                str(tmp_path / "o.jsonl"),
                "--coluna-texto",
                "texto",
            ],
            saida_texto=msgs.append,
        )
        == 2
    )
    assert "coluna 'texto'" in msgs[-1]
