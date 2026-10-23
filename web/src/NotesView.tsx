import { Component, useMemo, type ReactNode } from "react";
import { renderNotes } from "./markdown";

interface BoundaryProps {
  /** The notes as written: shown when rendering them fails. */
  text: string;
  children: ReactNode;
}

/**
 * Defence in depth around the rendered notes: should rendering a note
 * ever throw, the note shows as written instead of taking the page down.
 * Other text tries rendering again.
 */
export class NotesBoundary extends Component<BoundaryProps, { failed: boolean; text: string }> {
  state = { failed: false, text: this.props.text };

  static getDerivedStateFromProps(props: BoundaryProps, state: { failed: boolean; text: string }) {
    return props.text === state.text ? null : { failed: false, text: props.text };
  }

  static getDerivedStateFromError() {
    return { failed: true };
  }

  render() {
    if (this.state.failed) return <p className="whitespace-pre-wrap wrap-anywhere">{this.props.text}</p>;
    return this.props.children;
  }
}

function Rendered({ text }: { text: string }) {
  return useMemo(() => renderNotes(text), [text]);
}

/** The notes rendered from their Markdown (mounted only while they are not edited). */
export function NotesView({ text }: { text: string }) {
  return (
    <NotesBoundary text={text}>
      <Rendered text={text} />
    </NotesBoundary>
  );
}
