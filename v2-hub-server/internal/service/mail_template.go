package service

import (
	"bytes"
	"fmt"
	htmltpl "html/template"
	texttpl "text/template"
)

// 邮件模板内置在后端（docs/03 §5.7 内容）：纯文本与简单 HTML 各一份，变量只做转义后的文本替换。

// mailVars 是模板变量。
type mailVars struct {
	Site    string
	Code    string
	Minutes int
	Action  string // 验证码用途的说明，如“注册账号”
	Title   string // 公告标题、工单主题
	Body    string // 公告正文（Markdown 源文本，按纯文本发出）
}

// mailContent 是渲染后的一封邮件。
type mailContent struct {
	Subject string
	Text    string
	HTML    string
}

type mailTemplate struct {
	subject *texttpl.Template
	text    *texttpl.Template
	html    *htmltpl.Template
}

const mailHTMLWrap = `<div style="font-family:-apple-system,'Segoe UI',sans-serif;font-size:14px;color:#34395e;line-height:1.7">%s<p style="color:#98a6ad;font-size:12px">— {{.Site}}</p></div>`

func mustMailTemplate(name, subject, text, html string) mailTemplate {
	return mailTemplate{
		subject: texttpl.Must(texttpl.New(name + ".subject").Parse(subject)),
		text:    texttpl.Must(texttpl.New(name + ".text").Parse(text)),
		html:    htmltpl.Must(htmltpl.New(name + ".html").Parse(fmt.Sprintf(mailHTMLWrap, html))),
	}
}

var (
	mailTplCode = mustMailTemplate("code",
		"【{{.Site}}】验证码 {{.Code}}",
		"你正在{{.Action}}，验证码是 {{.Code}}，{{.Minutes}} 分钟内有效。\n如果不是你本人操作，请忽略这封邮件。\n\n— {{.Site}}\n",
		`<p>你正在{{.Action}}，验证码是</p><p style="font-size:24px;font-weight:700;letter-spacing:4px;color:#6777ef">{{.Code}}</p><p>{{.Minutes}} 分钟内有效。如果不是你本人操作，请忽略这封邮件。</p>`)
	mailTplTest = mustMailTemplate("test",
		"【{{.Site}}】测试邮件",
		"这是一封测试邮件，说明 SMTP 配置可用。\n\n— {{.Site}}\n",
		`<p>这是一封测试邮件，说明 SMTP 配置可用。</p>`)
	mailTplAnnouncement = mustMailTemplate("announcement",
		"【{{.Site}}】{{.Title}}",
		"{{.Title}}\n\n{{.Body}}\n\n— {{.Site}}\n",
		`<h3>{{.Title}}</h3><div style="white-space:pre-wrap">{{.Body}}</div>`)
	mailTplTicketReply = mustMailTemplate("ticket_reply",
		"【{{.Site}}】工单有新回复",
		"你的工单“{{.Title}}”有新回复，请登录后查看。\n\n— {{.Site}}\n",
		`<p>你的工单“{{.Title}}”有新回复，请登录后查看。</p>`)
	mailTplTicketNew = mustMailTemplate("ticket_new",
		"【{{.Site}}】工单待处理",
		"工单“{{.Title}}”有用户的新消息，请到管理端处理。\n\n— {{.Site}}\n",
		`<p>工单“{{.Title}}”有用户的新消息，请到管理端处理。</p>`)
)

func renderMail(t mailTemplate, v mailVars) (mailContent, error) {
	var subject, text, html bytes.Buffer
	if err := t.subject.Execute(&subject, v); err != nil {
		return mailContent{}, fmt.Errorf("render mail subject: %w", err)
	}
	if err := t.text.Execute(&text, v); err != nil {
		return mailContent{}, fmt.Errorf("render mail text: %w", err)
	}
	if err := t.html.Execute(&html, v); err != nil {
		return mailContent{}, fmt.Errorf("render mail html: %w", err)
	}
	return mailContent{Subject: subject.String(), Text: text.String(), HTML: html.String()}, nil
}
