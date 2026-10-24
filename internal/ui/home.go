package ui

import (
	"image"
	"image/color"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/chat"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// Prototype sizes of the chat column (Home.dc.html).
const (
	chatColumn    = 760
	chatPad       = 28
	userMaxWidth  = 520
	replyMaxWidth = 600
)

// homePage is the chat (OC-71): the conversation, the session state with
// Stop, and the composer.
type homePage struct {
	m    *mainUI
	chat *chatModel
	card guideCard

	list    widget.List
	editor  widget.Editor
	sendBtn widget.Clickable
	micBtn  widget.Clickable
	stopBtn widget.Clickable
	// sels keeps message text selectable, one per message.
	sels []widget.Selectable
	// thumbs caches the texture of each kept screenshot by its (immutable)
	// pixel buffer; entries die with the next conversation.
	thumbs map[*image.RGBA]paint.ImageOp
}

func newHomePage(m *mainUI) *homePage {
	p := &homePage{m: m, chat: m.chat, card: guideCard{g: m.guide}}
	p.list.Axis = layout.Vertical
	p.editor.Submit = true
	return p
}

func (p *homePage) submit(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	p.editor.SetText("")
	p.chat.send(text)
}

func (p *homePage) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	p.chat.ensure()
	view, caps, failKey := p.chat.snapshot()
	connected := p.chat.be != nil

	p.editor.ReadOnly = !caps.Text
	for {
		ev, ok := p.editor.Update(gtx)
		if !ok {
			break
		}
		if s, ok := ev.(widget.SubmitEvent); ok && caps.Text {
			p.submit(s.Text)
		}
	}
	if p.sendBtn.Clicked(gtx) && caps.Text {
		p.submit(p.editor.Text())
	}
	if p.micBtn.Clicked(gtx) && caps.Voice {
		p.chat.toggleVoice()
	}
	if p.stopBtn.Clicked(gtx) {
		p.chat.stop()
	}

	title := view.Title
	if title == "" {
		title = T(l, "chat.title.new")
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return kit.PageHeader(gtx, th, kit.PageTitle(th, title).Layout, func(gtx layout.Context) layout.Dimensions {
				return p.headerChips(gtx, th, caps)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = gtx.Constraints.Max
			switch {
			case !connected:
				return p.emptyState(gtx, th, icon.Unlink, T(l, "chat.off.title"), T(l, "chat.off.body"))
			case len(view.Messages) == 0 && !view.Phase.Busy():
				body := T(l, "chat.empty.body.text")
				if caps.Voice {
					body = T(l, "chat.empty.body.voice")
				}
				return p.emptyState(gtx, th, icon.Chat1, T(l, "chat.empty.title"), body)
			}
			return p.thread(gtx, th, l, view)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			// The walkthrough in progress stays in view above the composer
			// however far the conversation scrolls.
			if !p.card.active() {
				return layout.Dimensions{}
			}
			return layout.Inset{Top: th.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return column(gtx, th, func(gtx layout.Context) layout.Dimensions { return p.card.Layout(gtx, th, l) })
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return p.composer(gtx, th, l, view, caps, connected, failKey)
		}),
	)
}

func (p *homePage) headerChips(gtx layout.Context, th *theme.Theme, caps chat.Capabilities) layout.Dimensions {
	var chips []layout.FlexChild
	add := func(n icon.Name, txt string) {
		if txt == "" {
			return
		}
		if len(chips) > 0 {
			chips = append(chips, kit.HSpace(th.Dp(8)))
		}
		chips = append(chips, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Chip(gtx, th, n, txt) }))
	}
	add(icon.Sparkles, caps.Model)
	add(icon.Volume, caps.Speech)
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, chips...)
}

// column centres w in the chat column, as wide as the prototype's.
func column(gtx layout.Context, th *theme.Theme, w layout.Widget) layout.Dimensions {
	return layout.N.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(chatColumn)))
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Left: th.Dp(chatPad), Right: th.Dp(chatPad)}.Layout(gtx, w)
	})
}

func (p *homePage) emptyState(gtx layout.Context, th *theme.Theme, n icon.Name, title, body string) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(440)))
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.IconTile(gtx, th, n, th.Pal.AccentText) }),
			kit.VSpace(th.Dp(16)),
			layout.Rigid(kit.Title(th, title).Layout),
			kit.VSpace(th.Dp(8)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lb := kit.Lead(th, body)
				lb.Alignment = text.Middle
				return lb.Layout(gtx)
			}),
		)
	})
}

// thread lists the messages, then the live state of the session: the
// partial transcript while listening, or a status row with Stop.
func (p *homePage) thread(gtx layout.Context, th *theme.Theme, l Lang, v chat.View) layout.Dimensions {
	for len(p.sels) < len(v.Messages) {
		p.sels = append(p.sels, widget.Selectable{})
	}
	last := len(v.Messages) - 1
	streamingLast := last >= 0 && v.Messages[last].Streaming
	listening := v.Phase == chat.PhaseListening || v.Phase == chat.PhaseTranscribing
	// A separate status item is needed unless the streaming reply shows it.
	statusItem := v.Phase.Busy() && !(streamingLast && (v.Phase == chat.PhaseResponding || v.Phase == chat.PhaseSpeaking))
	n := len(v.Messages)
	if statusItem {
		n++
	}
	// A short conversation starts at the top as in the prototype; once it
	// overflows, the list follows new text while the user is at its end.
	pos := p.list.Position
	overflow := pos.First > 0 || pos.Offset > 0 || pos.OffsetLast < 0
	if overflow != p.list.ScrollToEnd {
		p.list.ScrollToEnd = overflow
		p.list.Position.BeforeEnd = false
		gtx.Execute(op.InvalidateCmd{})
	}
	return material.List(th.Theme, &p.list).Layout(gtx, n, func(gtx layout.Context, i int) layout.Dimensions {
		top := unit.Dp(0)
		if i == 0 {
			top = th.Dp(chatPad)
		}
		return layout.Inset{Top: top, Bottom: th.Dp(20)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return column(gtx, th, func(gtx layout.Context) layout.Dimensions {
				if i == len(v.Messages) {
					if listening {
						return p.pendingVoice(gtx, th, l, v)
					}
					return p.reply(gtx, th, l, chat.Message{Role: chat.RoleAssistant, Streaming: true}, nil, v.Phase)
				}
				m := v.Messages[i]
				if m.Role == chat.RoleUser {
					return p.userMessage(gtx, th, l, m, &p.sels[i])
				}
				phase := chat.PhaseIdle
				if i == last && m.Streaming {
					phase = v.Phase
				}
				return p.reply(gtx, th, l, m, &p.sels[i], phase)
			})
		})
	})
}

func (p *homePage) userMessage(gtx layout.Context, th *theme.Theme, l Lang, m chat.Message, sel *widget.Selectable) layout.Dimensions {
	meta := []string{T(l, "chat.via.text")}
	metaIcon := icon.Name("")
	if m.Via == chat.ViaVoice {
		meta = []string{T(l, "chat.via.voice")}
		if m.STTModel != "" {
			meta = append(meta, m.STTModel)
		}
		metaIcon = icon.Mic
	}
	if !m.At.IsZero() {
		meta = append(meta, m.At.Local().Format("15:04"))
	}
	return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(userMaxWidth)))
		gtx.Constraints.Min.X = 0
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return kit.Bubble(gtx, th, th.Pal.AccentStrong, func(gtx layout.Context) layout.Dimensions {
					b := kit.Body(th, m.Text)
					b.State = sel
					return b.Layout(gtx)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return p.screens(gtx, th, l, m.Screens)
			}),
			kit.VSpace(th.Dp(6)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return metaRow(gtx, th, metaIcon, strings.Join(meta, " · "), th.Pal.Muted)
			}),
		)
	})
}

// screens shows that the request carried a screenshot: a chip, and the
// thumbnail only if the user chose to keep screenshots. Without the opt-in
// there are no pixels left to show once the request is over.
func (p *homePage) screens(gtx layout.Context, th *theme.Theme, l Lang, shots []chat.Screenshot) layout.Dimensions {
	if len(shots) == 0 {
		return layout.Dimensions{}
	}
	kept := shots[0].Thumb
	key := "chat.shot.attached"
	if kept != nil {
		key = "chat.shot.kept"
	}
	return layout.Inset{Top: th.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if kept == nil {
					return layout.Dimensions{}
				}
				op, ok := p.thumbs[kept]
				if !ok {
					if p.thumbs == nil || len(p.thumbs) > 64 {
						p.thumbs = map[*image.RGBA]paint.ImageOp{}
					}
					op = paint.NewImageOp(kept)
					p.thumbs[kept] = op
				}
				return layout.Inset{Bottom: th.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return kit.Box(gtx, th, color.NRGBA{}, th.Pal.Divider, 6, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, gtx.Dp(th.Dp(120)))
						gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(220)))
						return widget.Image{Src: op, Fit: widget.ScaleDown}.Layout(gtx)
					})
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return kit.Chip(gtx, th, icon.Attachment, T(l, key))
			}),
		)
	})
}

// pendingVoice is the user's turn while it is being spoken: the live
// transcript, if any, and the state with Stop.
func (p *homePage) pendingVoice(gtx layout.Context, th *theme.Theme, l Lang, v chat.View) layout.Dimensions {
	return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(userMaxWidth)))
		gtx.Constraints.Min.X = 0
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if v.Partial == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Bottom: th.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return kit.Bubble(gtx, th, th.Pal.AccentSoft, func(gtx layout.Context) layout.Dimensions {
						b := kit.Body(th, v.Partial)
						b.Color = th.Pal.Fg2
						return b.Layout(gtx)
					})
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return p.statusRow(gtx, th, l, v.Phase) }),
		)
	})
}

func (p *homePage) reply(gtx layout.Context, th *theme.Theme, l Lang, m chat.Message, sel *widget.Selectable, phase chat.Phase) layout.Dimensions {
	gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(replyMaxWidth)))
	gtx.Constraints.Min.X = 0
	return layout.Flex{}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Avatar(gtx, th, m.Streaming) }),
		kit.HSpace(th.Dp(12)),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			var rows []layout.FlexChild
			if m.Text != "" {
				rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					// Align the first line with the avatar's centre.
					return layout.Inset{Top: th.Dp(3)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						b := kit.Body(th, m.Text)
						b.LineHeightScale = 1.6
						b.State = sel
						return b.Layout(gtx)
					})
				}))
			}
			extra := func(w layout.Widget) {
				if len(rows) > 0 {
					rows = append(rows, kit.VSpace(th.Dp(8)))
				}
				rows = append(rows, layout.Rigid(w))
			}
			switch {
			case m.Streaming && phase.Busy():
				extra(func(gtx layout.Context) layout.Dimensions { return p.statusRow(gtx, th, l, phase) })
			case m.Failed:
				extra(func(gtx layout.Context) layout.Dimensions {
					code := ""
					if m.Code != "" {
						code = Tf(l, "chat.failedCode", m.Code)
					}
					return kit.Alert(gtx, th, theme.StatusError, T(l, "chat.failed"), code)
				})
			case m.Interrupted:
				extra(func(gtx layout.Context) layout.Dimensions {
					return metaRow(gtx, th, icon.Stop, T(l, "chat.interrupted"), th.Pal.Muted)
				})
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		}),
	)
}

// statusRow shows what the session is doing, by icon and word, with Stop.
func (p *homePage) statusRow(gtx layout.Context, th *theme.Theme, l Lang, phase chat.Phase) layout.Dimensions {
	n, key := phaseLook(phase)
	h := gtx.Dp(th.Dp(30))
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return kit.VCenter(gtx, h, func(gtx layout.Context) layout.Dimensions { return kit.Icon(gtx, th, n, 14, th.Pal.AccentText) })
		}),
		kit.HSpace(th.Dp(8)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return kit.VCenter(gtx, h, kit.Text(th, 12, T(l, key), th.Pal.AccentText).Layout)
		}),
		kit.HSpace(th.Dp(10)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !phase.Busy() {
				return layout.Dimensions{}
			}
			return kit.ButtonStyle{Kind: kit.Secondary, Label: T(l, "chat.stop"), Icon: icon.Stop, Small: true}.Layout(gtx, th, &p.stopBtn)
		}),
	)
}

// phaseLook is the icon and catalogue key of a session phase.
func phaseLook(ph chat.Phase) (icon.Name, string) {
	switch ph {
	case chat.PhaseListening:
		return icon.Mic, "chat.status.listening"
	case chat.PhaseTranscribing:
		return icon.Mic, "chat.status.transcribing"
	case chat.PhaseThinking:
		return icon.Loading3, "chat.status.thinking"
	case chat.PhaseResponding:
		return icon.Sparkles, "chat.status.responding"
	case chat.PhaseSpeaking:
		return icon.Voice, "chat.status.speaking"
	case chat.PhaseAwaiting:
		return icon.Chat1, "chat.status.awaiting"
	}
	return icon.Check, "chat.status.ready"
}

func metaRow(gtx layout.Context, th *theme.Theme, n icon.Name, txt string, col color.NRGBA) layout.Dimensions {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if n == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Right: th.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return kit.Icon(gtx, th, n, 11, col)
			})
		}),
		layout.Rigid(kit.Text(th, 11.5, txt, col).Layout),
	)
}

func (p *homePage) composer(gtx layout.Context, th *theme.Theme, l Lang, v chat.View, caps chat.Capabilities, connected bool, failKey string) layout.Dimensions {
	if !caps.Text {
		gtx = gtx.Disabled()
	}
	hint := T(l, "chat.placeholder")
	if !connected {
		hint = T(l, "chat.placeholderOff")
	}
	recording := v.Phase == chat.PhaseListening
	return layout.Inset{Top: th.Dp(12), Bottom: th.Dp(20)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return column(gtx, th, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if failKey == "" {
						return layout.Dimensions{}
					}
					return layout.Inset{Bottom: th.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return kit.Alert(gtx, th, theme.StatusError, T(l, failKey), "")
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					border := th.Pal.Border
					if gtx.Focused(&p.editor) {
						border = th.Pal.AccentLine
					}
					return kit.Box(gtx, th, th.Pal.Surface, border, 10, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Top: th.Dp(14), Bottom: th.Dp(6), Left: th.Dp(16), Right: th.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									gtx.Constraints.Min.X = gtx.Constraints.Max.X
									// Two lines tall like the prototype's textarea; grows to six.
									line := gtx.Sp(th.Sp(15.0 / 14 * 1.5))
									gtx.Constraints.Min.Y = 2 * line
									gtx.Constraints.Max.Y = 6 * line
									e := material.Editor(th.Theme, &p.editor, hint)
									e.TextSize = th.Sp(15.0 / 14)
									e.LineHeightScale = 1.5
									e.Color = th.Pal.Fg
									e.HintColor = th.Pal.Subtle
									e.SelectionColor = th.Pal.Selected
									return e.Layout(gtx)
								})
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Top: th.Dp(6), Bottom: th.Dp(8), Left: th.Dp(12), Right: th.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return p.toolbar(gtx, th, l, caps, recording)
								})
							}),
						)
					})
				}),
				kit.VSpace(th.Dp(8)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					t := kit.Text(th, 11.5, footnote(l, caps, connected), th.Pal.Subtle)
					t.Alignment = text.Middle
					return t.Layout(gtx)
				}),
			)
		})
	})
}

func (p *homePage) toolbar(gtx layout.Context, th *theme.Theme, l Lang, caps chat.Capabilities, recording bool) layout.Dimensions {
	h := gtx.Dp(th.Dp(36))
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return kit.VCenter(gtx, h, kit.Small(th, T(l, "chat.enterHint")).Layout)
			})
		}),
		kit.HSpace(th.Dp(10)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			kind, n, desc := kit.RoundSecondary, icon.Mic, T(l, "chat.speak")
			switch {
			case !caps.Voice:
				gtx = gtx.Disabled()
				n, desc = icon.MicOff, T(l, "chat.voiceOff")
			case recording:
				kind, desc = kit.RoundActive, T(l, "chat.speakStop")
			}
			return kit.RoundIconButton(gtx, th, &p.micBtn, n, desc, kind)
		}),
		kit.HSpace(th.Dp(8)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return kit.RoundIconButton(gtx, th, &p.sendBtn, icon.ArrowUp, T(l, "chat.send"), kit.RoundPrimary)
		}),
	)
}

// footnote is the line under the composer: how to speak and where audio
// goes, only as far as it is true for the current setup.
func footnote(l Lang, caps chat.Capabilities, connected bool) string {
	switch {
	case !connected:
		return T(l, "chat.hint.offline")
	case !caps.Voice:
		return T(l, "chat.hint.noVoice")
	case caps.Hotkey != "":
		return Tf(l, "chat.hint.hotkey", caps.Hotkey) + " · " + T(l, "chat.hint.local")
	}
	return T(l, "chat.hint.local")
}

// sidebarStatus is the sidebar's bottom block: the session state by icon
// and word, and the Talk hotkey.
func sidebarStatus(gtx layout.Context, th *theme.Theme, l Lang, c *chatModel) layout.Dimensions {
	v, caps, _ := c.snapshot()
	n, key, col := icon.Unlink, "chat.status.offline", th.Pal.Muted
	if c.be != nil {
		n, key = phaseLook(v.Phase)
		col = th.Pal.AccentText
		if v.Phase == chat.PhaseIdle {
			col = th.Pal.OK
			if !caps.Voice {
				key = "chat.status.readyText"
			}
		}
	}
	return layout.Inset{Top: th.Dp(14), Bottom: th.Dp(14), Left: th.Dp(16), Right: th.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(th.Dp(22))
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return kit.VCenter(gtx, h, func(gtx layout.Context) layout.Dimensions { return kit.Icon(gtx, th, n, 14, col) })
			}),
			kit.HSpace(th.Dp(8)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				t := kit.Text(th, 12.5, T(l, key), th.Pal.Fg)
				t.MaxLines = 1
				return kit.VCenter(gtx, h, t.Layout)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if caps.Hotkey == "" || c.be == nil {
					return layout.Dimensions{}
				}
				return kit.VCenter(gtx, h, func(gtx layout.Context) layout.Dimensions { return kit.Kbd(gtx, th, caps.Hotkey) })
			}),
		)
	})
}
