// Keep the textual source available if rendering fails or JavaScript is disabled.
window.addEventListener('DOMContentLoaded', async () => {
  const diagrams = [...document.querySelectorAll('code.language-mermaid')];
  if (!diagrams.length) return;
  const dark = ['navy', 'coal', 'ayu'].some(name => document.documentElement.classList.contains(name));
  mermaid.initialize({startOnLoad: false, securityLevel: 'strict', theme: dark ? 'dark' : 'default'});
  for (const [index, source] of diagrams.entries()) {
    try {
      const {svg} = await mermaid.render(`pulse-diagram-${index}`, source.textContent);
      const figure = document.createElement('div');
      figure.className = 'pulse-diagram';
      figure.innerHTML = svg;
      source.parentElement.replaceWith(figure);
    } catch (error) {
      console.error('Pulse diagram failed to render', error);
      document.documentElement.dataset.diagramError = 'true';
    }
  }
});
