// Configuration for the /try page. No secrets belong here: this file is public.
//
// server: the public Voxie server the "Talk to Voxie" button connects to,
//   e.g. "https://demo.voxie.example". Empty means there is no public demo
//   backend yet: visitors connect to a Voxie they run themselves.
//   Voxie's /whip endpoint must be reachable over HTTPS from the browser, with
//   TURN configured (server.public_ip / turn_secret) so audio crosses NAT.
// video: the demo video, a URL to an .mp4/.webm file (e.g. "/media/demo.mp4")
//   or a YouTube embed URL ("https://www.youtube-nocookie.com/embed/<id>").
//   Empty shows a placeholder.
window.VOXIE_TRY = {
  server: "",
  video: "/media/voxie-demo.mp4",
  poster: "/media/voxie-demo-poster.jpg",  // still shown before the video plays
};
