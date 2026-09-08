export namespace main {
	
	export class TranslationResult {
	    translatedText: string;
	    detectedSource: string;
	
	    static createFrom(source: any = {}) {
	        return new TranslationResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.translatedText = source["translatedText"];
	        this.detectedSource = source["detectedSource"];
	    }
	}

}

