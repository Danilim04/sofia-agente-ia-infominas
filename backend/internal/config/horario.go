package config

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // fuso embutido: imagens enxutas e Windows não têm o banco de fusos do sistema
)

// Horário de atendimento humano (E5.5, P7). Tudo configurável porque o horário
// real do WhatsApp ainda é decisão do produto: o painel do Desk só confirmou o
// expediente das mesas (seg–sex 08:00–18:00, docs/infodesk/10 §8 linha 8).

// Modos de FORA_DO_HORARIO_MODO.
const (
	ForaDoHorarioAvisar  = "avisar"  // a mensagem de aviso ao cliente vira a versão de fora do horário
	ForaDoHorarioIgnorar = "ignorar" // aviso normal, como se estivesse no horário
)

// HorarioPadrao é o expediente do Desk. TODO(P7): confirmar com a Infominas que
// vale para o atendimento humano do WhatsApp.
const HorarioPadrao = "seg-sex 08:00-18:00"

// FusoPadrao é o fuso do horário e dos feriados.
const FusoPadrao = "America/Sao_Paulo"

// faixa é um intervalo [ini, fim) em minutos desde 00:00. fim ≤ ini atravessa a
// meia-noite (a faixa 22:00–06:00 vale de 22:00 até 06:00 do dia seguinte).
type faixa struct{ ini, fim int }

func (f faixa) viraODia() bool { return f.fim <= f.ini }

func (f faixa) texto() string { return hhmm(f.ini) + " às " + hhmm(f.fim) }

func hhmm(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

// Horario é o horário de atendimento humano. O zero value (e um *Horario nil)
// é "sempre dentro do horário".
type Horario struct {
	loc      *time.Location
	dias     [7][]faixa // índice 0 = segunda … 6 = domingo
	feriados map[string]bool
}

// nomes dos dias (índice 0 = segunda), em ordem, para o parse e para a descrição.
var (
	diasCurtos = [7]string{"seg", "ter", "qua", "qui", "sex", "sab", "dom"}
	diasLongos = [7]string{"segunda", "terca", "quarta", "quinta", "sexta", "sabado", "domingo"}
	diasPlural = [7]string{"às segundas", "às terças", "às quartas", "às quintas", "às sextas", "aos sábados", "aos domingos"}
	diasNome   = [7]string{"segunda", "terça", "quarta", "quinta", "sexta", "sábado", "domingo"}
)

var semAcentoHorario = strings.NewReplacer("á", "a", "à", "a", "â", "a", "ã", "a", "é", "e", "ê", "e", "í", "i", "ó", "o", "ô", "o", "õ", "o", "ú", "u", "ç", "c")

func indiceDoDia(s string) (int, bool) {
	s = semAcentoHorario.Replace(strings.ToLower(strings.TrimSpace(s)))
	for i := range diasCurtos {
		if s == diasCurtos[i] || s == diasLongos[i] {
			return i, true
		}
	}
	return 0, false
}

func indiceDoWeekday(d time.Weekday) int { return (int(d) + 6) % 7 }

// ParseHorario lê "seg-sex 08:00-18:00; sab 08:00-12:00". Cada entrada é
// `dias faixas`: dias = um dia (`sab`), um intervalo (`seg-sex`; pode dar a volta
// na semana: `sex-seg`) ou uma lista (`seg,qua,sex`; itens podem ser intervalos);
// faixas = `HH:MM-HH:MM` separadas por vírgula (`08:00-12:00,14:00-18:00`). Fim
// menor que o início atravessa a meia-noite; `24:00` vale como fim. Entradas do
// mesmo dia se somam. Texto vazio devolve nil (= sempre dentro).
func ParseHorario(texto string, loc *time.Location, feriados []string) (*Horario, error) {
	texto = strings.TrimSpace(texto)
	if texto == "" {
		return nil, nil
	}
	h := &Horario{loc: loc, feriados: map[string]bool{}}
	for _, f := range feriados {
		h.feriados[f] = true
	}
	for _, entrada := range strings.Split(texto, ";") {
		entrada = strings.TrimSpace(entrada)
		if entrada == "" {
			continue
		}
		campos := strings.Fields(entrada)
		if len(campos) < 2 {
			return nil, fmt.Errorf("entrada %q: use `dias faixas`, ex.: seg-sex 08:00-18:00", entrada)
		}
		dias, err := parseDias(campos[0])
		if err != nil {
			return nil, fmt.Errorf("entrada %q: %w", entrada, err)
		}
		faixas, err := parseFaixas(strings.Join(campos[1:], ""))
		if err != nil {
			return nil, fmt.Errorf("entrada %q: %w", entrada, err)
		}
		for _, d := range dias {
			h.dias[d] = append(h.dias[d], faixas...)
		}
	}
	vazio := true
	for _, d := range h.dias {
		if len(d) > 0 {
			vazio = false
		}
	}
	if vazio {
		return nil, fmt.Errorf("nenhuma faixa de horário")
	}
	return h, nil
}

func parseDias(s string) ([]int, error) {
	var out []int
	for _, item := range strings.Split(s, ",") {
		if item == "" {
			return nil, fmt.Errorf("lista de dias com item vazio")
		}
		if a, b, ok := strings.Cut(item, "-"); ok {
			ia, oka := indiceDoDia(a)
			ib, okb := indiceDoDia(b)
			if !oka || !okb {
				return nil, fmt.Errorf("dia desconhecido em %q (use seg, ter, qua, qui, sex, sab, dom)", item)
			}
			for d := ia; ; d = (d + 1) % 7 {
				out = append(out, d)
				if d == ib {
					break
				}
			}
			continue
		}
		d, ok := indiceDoDia(item)
		if !ok {
			return nil, fmt.Errorf("dia desconhecido %q (use seg, ter, qua, qui, sex, sab, dom)", item)
		}
		out = append(out, d)
	}
	return out, nil
}

func parseFaixas(s string) ([]faixa, error) {
	var out []faixa
	for _, item := range strings.Split(s, ",") {
		a, b, ok := strings.Cut(item, "-")
		if !ok {
			return nil, fmt.Errorf("faixa %q: use HH:MM-HH:MM", item)
		}
		ini, err := parseMinutos(a, false)
		if err != nil {
			return nil, fmt.Errorf("faixa %q: %w", item, err)
		}
		fim, err := parseMinutos(b, true)
		if err != nil {
			return nil, fmt.Errorf("faixa %q: %w", item, err)
		}
		if ini == fim {
			return nil, fmt.Errorf("faixa %q: início e fim iguais", item)
		}
		out = append(out, faixa{ini, fim})
	}
	return out, nil
}

// parseMinutos lê HH:MM (00:00–23:59; 24:00 só como fim).
func parseMinutos(s string, fim bool) (int, error) {
	hs, ms, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, fmt.Errorf("horário %q: use HH:MM", s)
	}
	h, errH := strconv.Atoi(hs)
	m, errM := strconv.Atoi(ms)
	if errH != nil || errM != nil || len(ms) != 2 || m < 0 || m > 59 || h < 0 {
		return 0, fmt.Errorf("horário %q: use HH:MM", s)
	}
	if h > 24 || (h == 24 && (m != 0 || !fim)) {
		return 0, fmt.Errorf("horário %q fora de 00:00–23:59 (24:00 só como fim)", s)
	}
	return h*60 + m, nil
}

// Dentro diz se `t` cai dentro do horário de atendimento humano. Um *Horario nil
// é "sempre dentro". Feriado (data no fuso do horário) é fora o dia inteiro; uma
// faixa que atravessa a meia-noite pertence ao dia em que começou.
func (h *Horario) Dentro(t time.Time) bool {
	if h == nil {
		return true
	}
	t = t.In(h.loc)
	minuto := t.Hour()*60 + t.Minute()
	hoje := indiceDoWeekday(t.Weekday())
	if !h.feriados[t.Format("2006-01-02")] {
		for _, f := range h.dias[hoje] {
			if !f.viraODia() && minuto >= f.ini && minuto < f.fim {
				return true
			}
			if f.viraODia() && minuto >= f.ini {
				return true
			}
		}
	}
	ontem := t.AddDate(0, 0, -1)
	if !h.feriados[ontem.Format("2006-01-02")] {
		for _, f := range h.dias[(hoje+6)%7] {
			if f.viraODia() && minuto < f.fim {
				return true
			}
		}
	}
	return false
}

// TemFeriados diz se há feriados configurados.
func (h *Horario) TemFeriados() bool { return h != nil && len(h.feriados) > 0 }

// Descrever escreve o horário em português para a mensagem ao cliente, só com o
// que foi configurado (nenhuma hora inventada): "de segunda a sexta, das 08:00 às
// 18:00; aos sábados, das 08:00 às 12:00". Vazio para nil.
func (h *Horario) Descrever() string {
	if h == nil {
		return ""
	}
	// Agrupa os dias que têm as mesmas faixas (ordenadas), na ordem da semana.
	type grupo struct {
		dias   []int
		faixas string
	}
	var grupos []grupo
	for d := 0; d < 7; d++ {
		if len(h.dias[d]) == 0 {
			continue
		}
		fs := append([]faixa(nil), h.dias[d]...)
		sort.Slice(fs, func(i, j int) bool { return fs[i].ini < fs[j].ini })
		partes := make([]string, 0, len(fs))
		for _, f := range fs {
			partes = append(partes, "das "+f.texto())
		}
		txt := strings.Join(partes, " e ")
		achou := false
		for i := range grupos {
			if grupos[i].faixas == txt {
				grupos[i].dias = append(grupos[i].dias, d)
				achou = true
				break
			}
		}
		if !achou {
			grupos = append(grupos, grupo{dias: []int{d}, faixas: txt})
		}
	}
	out := make([]string, 0, len(grupos))
	for _, g := range grupos {
		out = append(out, descreverDias(g.dias)+", "+g.faixas)
	}
	return strings.Join(out, "; ")
}

// descreverDias: um dia → "aos sábados"; sequência de 3+ → "de segunda a sexta";
// o resto → "segunda, quarta e sexta".
func descreverDias(dias []int) string {
	if len(dias) == 1 {
		return diasPlural[dias[0]]
	}
	seguidos := true
	for i := 1; i < len(dias); i++ {
		if dias[i] != dias[i-1]+1 {
			seguidos = false
		}
	}
	if seguidos && len(dias) >= 3 {
		return "de " + diasNome[dias[0]] + " a " + diasNome[dias[len(dias)-1]]
	}
	nomes := make([]string, len(dias))
	for i, d := range dias {
		nomes[i] = diasNome[d]
	}
	if len(nomes) == 2 {
		return nomes[0] + " e " + nomes[1]
	}
	return strings.Join(nomes[:len(nomes)-1], ", ") + " e " + nomes[len(nomes)-1]
}

// parseFeriados lê FERIADOS: datas AAAA-MM-DD separadas por vírgula, ponto e
// vírgula ou espaço.
func parseFeriados(bruto string) ([]string, error) {
	var out []string
	for _, item := range strings.FieldsFunc(bruto, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' }) {
		d, err := time.Parse("2006-01-02", item)
		if err != nil {
			return nil, fmt.Errorf("FERIADOS inválido: %q não é uma data AAAA-MM-DD", item)
		}
		out = append(out, d.Format("2006-01-02"))
	}
	return out, nil
}

// carregarHorario lê HORARIO_HUMANO (ausente = HorarioPadrao; presente e vazia =
// sempre dentro do horário), HORARIO_FUSO, FERIADOS e FORA_DO_HORARIO_MODO.
func (c *Config) carregarHorario() error {
	fuso := FusoPadrao
	if v, ok := os.LookupEnv("HORARIO_FUSO"); ok && strings.TrimSpace(v) != "" {
		fuso = strings.TrimSpace(v)
	}
	loc, err := time.LoadLocation(fuso)
	if err != nil {
		return fmt.Errorf("HORARIO_FUSO inválido (%q): use um nome IANA, ex.: America/Sao_Paulo", fuso)
	}
	var feriados []string
	if v, ok := os.LookupEnv("FERIADOS"); ok {
		if feriados, err = parseFeriados(v); err != nil {
			return err
		}
	}
	texto := HorarioPadrao
	if v, ok := os.LookupEnv("HORARIO_HUMANO"); ok {
		texto = v
	}
	if c.Horario, err = ParseHorario(texto, loc, feriados); err != nil {
		return fmt.Errorf("HORARIO_HUMANO inválido: %w", err)
	}
	if c.Horario != nil {
		c.HorarioDescricao = c.Horario.Descrever()
	}
	c.ForaDoHorarioModo = ForaDoHorarioAvisar
	if v, ok := os.LookupEnv("FORA_DO_HORARIO_MODO"); ok && strings.TrimSpace(v) != "" {
		c.ForaDoHorarioModo = strings.ToLower(strings.TrimSpace(v))
	}
	if c.ForaDoHorarioModo != ForaDoHorarioAvisar && c.ForaDoHorarioModo != ForaDoHorarioIgnorar {
		return fmt.Errorf("FORA_DO_HORARIO_MODO inválido (%q): use %q ou %q", c.ForaDoHorarioModo, ForaDoHorarioAvisar, ForaDoHorarioIgnorar)
	}
	return nil
}
