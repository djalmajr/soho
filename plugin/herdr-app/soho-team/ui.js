// Small gpui-kit building blocks for the SoHo panel. Theme names only: no
// proposal hex values and no hard-coded colors. UI only; no state or commands.
import { div, svg } from "gpui-kit";
import { h_flex, v_flex, Popover, TextView } from "gpui-base";
import { theme } from "herdr";

export const TEXT = { caption: 11, small: 12, tab: 12.8, body: 13, title: 15 };

export const trunc = (text, width, color, size = TEXT.small) =>
  div().min_w(0).flex_none().w(width).overflow_hidden().text_ellipsis().whitespace_nowrap()
    .text_size(size).text_color(color).child(text ?? "");

export const growText = (text, color, size = TEXT.small) =>
  div().min_w(0).flex_1().truncate().text_size(size).text_color(color).child(text ?? "");

export function button(id, label, action, opts = {}) {
  const t = theme();
  const { primary = false, small = false, disabled = false, warn = false } = opts;
  const bg = disabled ? t.background : primary ? t.solid : warn ? t.raised : t.input;
  const fg = disabled ? t.dim : primary ? t.solid_foreground : t.foreground;
  return h_flex().id(id).h(small ? 24 : 28).px(small ? 8 : 12).flex_none().items_center().justify_center()
    .rounded(small ? 5 : 7).border_1().border_color(disabled ? t.border : primary ? t.solid : t.border)
    .bg(bg).text_color(fg).text_size(small ? TEXT.caption : TEXT.small).font_medium()
    .when(!disabled, (el) => el.cursor_pointer().hover((s) => s.bg(t.hover).opacity(0.9)))
    .when(disabled, (el) => el.opacity(0.55))
    .when(!disabled, (el) => el.on_click((_e, cx) => action(cx)))
    .child(label);
}

export function chip(text, t, color) {
  if (!text) return div();
  return div().flex_none().h(22).px(7).items_center().rounded(4).border_1().border_color(t.border)
    .bg(t.raised).text_size(TEXT.caption).text_color(color ?? t.foreground).child(String(text));
}

export function dot(color, t) {
  return div().w(8).h(8).flex_none().rounded(4).bg(color ?? t.unknown);
}

export function sectionTitle(text, t) {
  return div().pt(6).pb(3).text_size(TEXT.caption).font_semibold().text_color(t.dim).child(text);
}

export function errorBlock(text, t) {
  if (!text) return div();
  return div().id(`err-${text.length}`).px(8).py(6).rounded(6).border_1().border_color(t.border)
    .bg(t.background).text_size(TEXT.caption).text_color(t.ansi1).child(text);
}

export function notice(text, undo, onUndo, t, opts = {}) {
  const { disabled = false } = opts;
  return h_flex().id("soho-notice").min_h(30).px(10).py(5).gap(8).items_center()
    .rounded(7).border_1().border_color(t.border).bg(t.raised)
    .child(growText(text, t.foreground, TEXT.caption))
    .when(Boolean(undo), (el) => el.child(button("soho-undo", "Undo", onUndo, { small: true, disabled })));
}

// A controlled value-with-caret chooser. `choices` are {id, label}; Popover
// owns press, outside dismissal and Escape; the check is drawn, not just ARIA.
export function chooser({ id, label, choices, value, open, note, focus, width = 150, small = true, disabled = false, onOpenChange, onChoose }) {
  const t = theme();
  const trigger = h_flex().id(`${id}-trigger`).h(small ? 24 : 32).w(width).flex_none().px(8)
    .gap(6).items_center().rounded(5).border_1().border_color(open ? t.accent : t.input)
    .bg(t.background).text_size(small ? TEXT.caption : TEXT.small)
    .when(!disabled, (el) => el.cursor_pointer().hover((s) => s.bg(t.hover)))
    .when(disabled, (el) => el.opacity(0.55))
    .child(growText(label ?? value ?? "", t.foreground, small ? TEXT.caption : TEXT.small))
    .child(div().flex_none().text_color(t.dim).child(open ? "▲" : "▼"));
  const options = choices.map((choice) =>
    div().id(`${id}-option-${choice.id}`).flex_none().min_h(26).px(8).items_center()
      .gap(6).rounded(5).role("list_box_option").aria_selected(choice.id === value)
      .cursor_pointer().hover((s) => s.bg(t.hover))
      .when(!disabled, (el) => el.on_click((_e, cx) => onChoose(choice, cx)))
      .child(growText(choice.label, t.foreground, TEXT.caption))
      .when(choice.id === value, (el) => el.child(svg("mods/workspace-chrome/check.svg").size(11).text_color(t.accent))));
  return Popover.new(id)
    .accessibility_label(`${id} chooser`)
    .anchor("bottom_left")
    .open(Boolean(open) && !disabled)
    .track_focus(focus)
    .on_open_change((next, cx) => { if (!disabled) onOpenChange(next, cx); })
    .trigger(trigger)
    .when(Boolean(open), (el) => el.content(
      v_flex().id(`${id}-menu`).w(width).max_h(280).p(4).gap(1).overflow_y_scrollbar()
        .rounded(7).border_1().border_color(t.border).bg(t.popover)
        .on_key_down((e, cx) => {
          if (e.key !== "escape") return;
          onOpenChange(false, cx);
          cx.stop_propagation();
        })
        .children(options)
        .when(Boolean(note), (el) => el.child(div().px(8).py(5).text_size(TEXT.caption).text_color(t.dim).child(note))),
    ));
}

export function segmented(id, choices, value, onChoose, t) {
  return h_flex().id(id).p(3).gap(3).rounded(7).bg(t.background)
    .children(choices.map((choice) =>
      div().id(`${id}-${choice.id}`).h(22).px(9).items_center().rounded(5).cursor_pointer()
        .bg(choice.id === value ? t.raised : t.background)
        .text_color(choice.id === value ? t.foreground : t.muted)
        .text_size(TEXT.caption)
        .hover((s) => s.text_color(t.foreground))
        .on_click((_e, cx) => onChoose(choice.id, cx))
        .child(choice.label)));
}

export function field(label, help, ...children) {
  const t = theme();
  return v_flex().gap(6)
    .child(div().text_size(TEXT.caption).font_semibold().text_color(t.muted).child(label))
    .children(children)
    .when(Boolean(help), (el) => el.child(div().text_size(TEXT.caption).text_color(t.dim).child(help)));
}

export function markdown(id, text) {
  return v_flex().flex_1().min_h(0).overflow_y_scrollbar()
    .child(TextView.markdown(id, text).selectable(true).scrollable(true));
}
