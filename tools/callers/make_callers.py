"""Generates test callers: three lines per language (hello / a question / goodbye)
as 48kHz WAVs, with Google TTS standing in for a person.

    pip install gTTS librosa soundfile
    python tools/callers/make_callers.py            # all languages
    python tools/callers/make_callers.py ta ko      # some
"""
import os
import sys

import librosa
import soundfile as sf
from gtts import gTTS

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "audio")

# code: (gTTS language, accent tld, hello, question, goodbye)
LINES = {
    "en": ("en", "com", "Hello, who is this?", "Can you tell me what time you open tomorrow, and do I need an appointment?", "No, that's all. Thank you, bye."),
    "en-in": ("en", "co.in", "Hello, who is this?", "Can you tell me what time you open tomorrow, and do I need an appointment?", "No, that's all. Thank you, bye."),
    "hi": ("hi", "co.in", "हेलो, कौन बोल रहा है?", "क्या आप बता सकते हैं कि कल आप कितने बजे खुलते हैं, और क्या मुझे अपॉइंटमेंट लेना होगा?", "नहीं, बस इतना ही। धन्यवाद, बाय।"),
    "hinglish": ("hi", "co.in", "हाँ हेलो, कौन बोल रहा है?", "कल आप कितने बजे open होते हो, और क्या appointment लेना पड़ेगा?", "नहीं, बस इतना ही, thank you, bye."),
    "ta": ("ta", "com", "ஹலோ, யார் பேசுவது?", "நாளை எத்தனை மணிக்கு திறப்பீர்கள், எனக்கு முன்பதிவு தேவையா என்று சொல்ல முடியுமா?", "இல்லை, அவ்வளவுதான். நன்றி."),
    "te": ("te", "com", "హలో, ఎవరు మాట్లాడుతున్నారు?", "రేపు మీరు ఎన్ని గంటలకు తెరుస్తారు, నాకు అపాయింట్‌మెంట్ అవసరమా చెప్పగలరా?", "లేదు, అంతే. ధన్యవాదాలు."),
    "bn": ("bn", "com", "হ্যালো, কে বলছেন?", "কাল আপনারা কটায় খোলেন, আর আমার কি অ্যাপয়েন্টমেন্ট লাগবে, বলতে পারবেন?", "না, এটুকুই। ধন্যবাদ।"),
    "mr": ("mr", "com", "हॅलो, कोण बोलतंय?", "उद्या तुम्ही किती वाजता उघडता, आणि मला अपॉइंटमेंट घ्यावी लागेल का, सांगू शकाल का?", "नाही, एवढंच. धन्यवाद."),
    "gu": ("gu", "com", "હેલો, કોણ બોલે છે?", "કાલે તમે કેટલા વાગ્યે ખોલો છો, અને મારે એપોઇન્ટમેન્ટ લેવી પડશે કે નહીં, કહી શકશો?", "ના, બસ એટલું જ. આભાર."),
    "kn": ("kn", "com", "ಹಲೋ, ಯಾರು ಮಾತನಾಡುತ್ತಿದ್ದೀರಿ?", "ನಾಳೆ ನೀವು ಎಷ್ಟು ಗಂಟೆಗೆ ತೆರೆಯುತ್ತೀರಿ, ನನಗೆ ಅಪಾಯಿಂಟ್‌ಮೆಂಟ್ ಬೇಕೇ ಎಂದು ಹೇಳಬಹುದೇ?", "ಇಲ್ಲ, ಅಷ್ಟೇ. ಧನ್ಯವಾದಗಳು."),
    "ml": ("ml", "com", "ഹലോ, ആരാണ് സംസാരിക്കുന്നത്?", "നാളെ നിങ്ങൾ എത്ര മണിക്കാണ് തുറക്കുന്നത്, എനിക്ക് അപ്പോയിന്റ്മെന്റ് വേണോ എന്ന് പറയാമോ?", "ഇല്ല, അത്രമാത്രം. നന്ദി."),
    "pa": ("pa", "com", "ਹੈਲੋ, ਕੌਣ ਬੋਲ ਰਿਹਾ ਹੈ?", "ਕੱਲ੍ਹ ਤੁਸੀਂ ਕਿੰਨੇ ਵਜੇ ਖੋਲ੍ਹਦੇ ਹੋ, ਅਤੇ ਕੀ ਮੈਨੂੰ ਅਪੌਇੰਟਮੈਂਟ ਚਾਹੀਦੀ ਹੈ, ਦੱਸ ਸਕਦੇ ਹੋ?", "ਨਹੀਂ, ਬੱਸ ਇੰਨਾ ਹੀ। ਧੰਨਵਾਦ।"),
    "es": ("es", "com", "Hola, ¿quién habla?", "¿Me puede decir a qué hora abren mañana y si necesito cita?", "No, eso es todo. Gracias, adiós."),
    "fr": ("fr", "com", "Allô, qui est à l'appareil ?", "Pouvez-vous me dire à quelle heure vous ouvrez demain, et s'il faut prendre rendez-vous ?", "Non, c'est tout. Merci, au revoir."),
    "de": ("de", "com", "Hallo, wer ist da?", "Können Sie mir sagen, wann Sie morgen öffnen und ob ich einen Termin brauche?", "Nein, das ist alles. Danke, tschüss."),
    "it": ("it", "com", "Pronto, chi parla?", "Può dirmi a che ora aprite domani e se serve un appuntamento?", "No, è tutto. Grazie, arrivederci."),
    "pt": ("pt", "com.br", "Alô, quem fala?", "Pode me dizer a que horas vocês abrem amanhã e se eu preciso marcar horário?", "Não, é só isso. Obrigado, tchau."),
    "ja": ("ja", "com", "もしもし、どなたですか？", "明日は何時に開きますか？予約は必要ですか？", "いいえ、それだけです。ありがとうございました、さようなら。"),
    "zh": ("zh-CN", "com", "喂，你好，请问是哪位？", "请问你们明天几点开门？需要预约吗？", "没有了，就这些，谢谢，再见。"),
    "ko": ("ko", "com", "여보세요, 누구세요?", "내일 몇 시에 문을 여는지, 예약이 필요한지 알려 주시겠어요?", "아니요, 그게 다예요. 감사합니다, 안녕히 계세요."),
    "ar": ("ar", "com", "ألو، من المتحدث؟", "هل يمكنك أن تخبرني متى تفتحون غداً، وهل أحتاج إلى موعد؟", "لا، هذا كل شيء. شكراً، مع السلامة."),
    "tr": ("tr", "com", "Alo, kim arıyor?", "Yarın saat kaçta açıyorsunuz, randevu almam gerekiyor mu, söyleyebilir misiniz?", "Hayır, bu kadar. Teşekkürler, hoşça kalın."),
}


def main() -> None:
    os.makedirs(OUT, exist_ok=True)
    for code in sys.argv[1:] or LINES:
        lang, tld, *lines = LINES[code]
        for name, text in zip(("hello", "ask", "bye"), lines):
            mp3 = os.path.join(OUT, f"{name}_{code}.mp3")
            gTTS(text, lang=lang, tld=tld).save(mp3)
            y, _ = librosa.load(mp3, sr=48000, mono=True)
            sf.write(os.path.join(OUT, f"{name}_{code}.wav"), y, 48000, subtype="PCM_16")
            os.remove(mp3)
        print(code, "ok")


if __name__ == "__main__":
    main()
